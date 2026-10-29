package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/coordinator"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// A drive body, a server and the names of the workers, is far shorter than
// this.
const maxDrive = 64 << 10

// The pointer has the URL a moment after cloudflared starts, or a minute
// after a failed publish; a second between tries shows ready soon after.
const proveRetry = time.Second

// Network states in GET /: no worker can find the harness yet, or one can.
const (
	starting = "starting"
	ready    = "ready"
)

// api is the routes of the harness's machine and config, beside the
// sessions' in harness.Handler:
//
//	GET    /               -> {"version","network"}: network is "ready" once a worker can
//	                          find the harness at its URL, else why the tunnel last stopped
//	                          the harness while it starts again, else "starting"
//	GET    /join           -> {"join"}: the network name and device code, as a worker
//	                          installs with them
//	POST   /keys           {"name"} -> {"name","key"}: a new api key; a name in use is 409
//	GET    /drives         -> the drives as the config has them, by name
//	PUT    /drives/{name}  {"server","workers"} -> 204: saved in the config and applied;
//	                          refused when its server cannot serve it now
//	DELETE /drives/{name}  -> 204
//	GET    /settings       -> {"version":{"current","latest"},"data_sharing"}
//	POST   /settings       {"data_sharing":{"share","via","text"}} -> 204: saved in the config
//	                       {"version"} -> the admin worker makes the harness that release
//	                          and restarts it, so a success never answers
type api struct {
	dir, path string
	k         ed25519.PrivateKey
	at        func() string // the harness's URL, or none yet
	proving   atomic.Value  // the URL prove last started on
	reached   atomic.Value  // the URL a worker last found the harness at
	coord     *coordinator.Coordinator
	pool      *harness.Pool
}

func (a *api) handle(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.root)
	mux.HandleFunc("GET /join", a.join)
	mux.HandleFunc("POST /keys", a.key)
	mux.HandleFunc("GET /drives", a.drives)
	mux.HandleFunc("PUT /drives/{name}", a.setDrive)
	mux.HandleFunc("DELETE /drives/{name}", a.deleteDrive)
	mux.HandleFunc("GET /settings", a.settings)
	mux.HandleFunc("POST /settings", a.setSettings)
}

func (a *api) root(w http.ResponseWriter, r *http.Request) {
	network := starting
	if u, _ := a.reached.Load().(string); u != "" && u == a.at() {
		network = ready
	} else if b, err := os.ReadFile(filepath.Join(a.dir, tunnelFailed)); err == nil {
		network = string(b)
	}
	reply(w, map[string]string{"version": version.V, "network": network})
}

// prove marks url reached once a worker can find the harness there, as
// joining does: the pointer names url, url proves the harness key and passes
// the WebSocket upgrade. It gives up when the harness moves to another URL. A
// tunnel that starts again at the same URL proves it once.
func (a *api) prove(ctx context.Context, url string) {
	if old, _ := a.proving.Swap(url).(string); old == url {
		return
	}
	for a.at() == url {
		if u, err := reach(ctx, pointer.Encode(a.k)); err == nil && u == url {
			a.reached.Store(url)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(proveRetry):
		}
	}
}

func (a *api) join(w http.ResponseWriter, r *http.Request) {
	n, err := LoadNetwork(a.dir)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, map[string]string{"join": n.String()})
}

func (a *api) key(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if err := json.NewDecoder(io.LimitReader(r.Body, maxKey)).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" || strings.ContainsAny(in.Name, "\r\n") {
		fail(w, errors.New(`the body is {"name"}, a name of one line`))
		return
	}
	secret, err := Auth(a.dir).Issue(auth.Key, in.Name)
	if errors.Is(err, auth.ErrTaken) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, map[string]string{"name": in.Name, "key": auth.Token(auth.Key, pointer.Encode(a.k), secret)})
}

func (a *api) drives(w http.ResponseWriter, r *http.Request) {
	c, err := loadConfig(a.path)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, orEmpty(c.Drives))
}

func (a *api) setDrive(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var d coordinator.Drive
	dec := json.NewDecoder(io.LimitReader(r.Body, maxDrive))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		fail(w, fmt.Errorf(`the body is {"server","workers"}: %w`, err))
		return
	}
	if err := a.serves(name, d); err != nil {
		fail(w, err)
		return
	}
	if err := a.edit(func(ds map[string]coordinator.Drive) error {
		ds[name] = d
		return nil
	}); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) deleteDrive(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.edit(func(ds map[string]coordinator.Drive) error {
		if _, ok := ds[name]; !ok {
			return fmt.Errorf("no drive %q", name)
		}
		delete(ds, name)
		return nil
	}); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serves is why drive d, named name, cannot be served now, or nil: its
// server must be a connected worker that says it can serve drives.
func (a *api) serves(name string, d coordinator.Drive) error {
	server, err := a.coord.Check(name, d)
	if err != nil {
		return err
	}
	ws := a.pool.List()
	i := slices.IndexFunc(ws, func(l harness.Listed) bool { return l.Name == server && l.Refused == "" })
	switch {
	case i < 0:
		return fmt.Errorf("drive %s: its server %s is not connected", name, server)
	case !ws[i].DriveServer:
		return fmt.Errorf("drive %s: %s cannot serve drives: a drive server is Debian or Ubuntu, with no other NFS export and port 445 free", name, server)
	}
	return nil
}

// edit changes the config's drives with fn, saves the config and applies
// them. keyMu holds the read and write against any other config write.
func (a *api) edit(fn func(map[string]coordinator.Drive) error) error {
	keyMu.Lock()
	defer keyMu.Unlock()
	c, err := Load(a.path)
	if err != nil {
		return err
	}
	c.Drives = orEmpty(c.Drives)
	if err := fn(c.Drives); err != nil {
		return err
	}
	if err := saveConfig(a.path, c); err != nil {
		return err
	}
	setDrives(a.coord, a.pool, c.Drives)
	return nil
}

// orEmpty is ds, or an empty map for none.
func orEmpty(ds map[string]coordinator.Drive) map[string]coordinator.Drive {
	if ds == nil {
		return map[string]coordinator.Drive{}
	}
	return ds
}
