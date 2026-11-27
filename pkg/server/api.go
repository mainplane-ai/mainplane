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

	"github.com/mainplane-ai/mainplane/pkg/api"
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

// machine is the routes of the harness's machine and config, beside the
// sessions' in harness.Handler:
//
//	GET    /               -> {"version","network"}: network is "ready" once a worker can
//	                          find the harness at its URL, else why the tunnel last stopped
//	                          the harness while it starts again, else "starting"
//	GET    /join           -> {"join"}: the network name and device code, as a worker
//	                          installs with them
//	POST   /keys           {"name"} -> {"name","key"}: a new api key; a name in use is 409
//	GET    /keys           -> [{"name","created","used","self","harness"}]: the api keys, never a secret
//	                          or its hash; self is the key that asks,
//	                          harness the default key
//	DELETE /keys/{name}    -> 204: the key revoked; the key that asks and the default key
//	                          are 409, an unknown name 404
//	POST   /workers/tokens {"name"} -> {"name","token"}: a join token that joins one machine,
//	                          as worker name; a name with a token is 409. A worker of that
//	                          name, online or not, refuses the join, and the token is spent
//	DELETE /workers/tokens/{name} -> 204: the unspent token of that name joins no machine
//	GET    /drives         -> the drives as the config has them, by name
//	PUT    /drives/{name}  {"server","workers"} -> {"linked"}: saved in the config and applied,
//	                          with the links it needs between its server and each worker,
//	                          which linked lists; refused when its server cannot serve it now
//	DELETE /drives/{name}  -> 204
//	GET    /settings       -> {"version":{"current","latest"},"data_sharing"}
//	POST   /settings       {"data_sharing":{"share","via","text"}} -> 204: saved in the config
//	                       {"version"} -> the admin worker makes the harness that release
//	                          and restarts it, so a success never answers
type machine struct {
	dir, path string
	k         ed25519.PrivateKey
	noPointer bool          // the config's NoPointer: join lines and keys carry the harness URL
	at        func() string // the harness's URL, or none yet
	proving   atomic.Value  // the URL prove last started on
	reached   atomic.Value  // the URL a worker last found the harness at
	coord     *coordinator.Coordinator
	pool      *harness.Pool
}

func (a *machine) handle(mux *http.ServeMux) {
	mux.HandleFunc("GET "+api.RootPath+"{$}", a.root)
	mux.HandleFunc("GET "+api.JoinPath, a.join)
	mux.HandleFunc("POST "+api.KeysPath, a.key)
	mux.HandleFunc("GET "+api.KeysPath, a.keys)
	mux.HandleFunc("DELETE "+api.KeyPath, a.revoke)
	mux.HandleFunc("POST "+api.JoinTokensPath, a.token)
	mux.HandleFunc("DELETE "+api.JoinTokenPath, func(w http.ResponseWriter, r *http.Request) {
		if err := Auth(a.dir).Revoke(auth.Join, r.PathValue("name")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+api.DrivesPath, a.drives)
	mux.HandleFunc("PUT "+api.DrivePath, a.setDrive)
	mux.HandleFunc("DELETE "+api.DrivePath, a.deleteDrive)
	mux.HandleFunc("GET "+api.SettingsPath, a.settings)
	mux.HandleFunc("POST "+api.SettingsPath, a.setSettings)
}

func (a *machine) root(w http.ResponseWriter, r *http.Request) {
	network, at := starting, a.at()
	if u, _ := a.reached.Load().(string); u != "" && u == at {
		network = ready
	} else if b, err := os.ReadFile(filepath.Join(a.dir, tunnelFailed)); err == nil {
		network = string(b)
	}
	n, err := LoadNetwork(a.dir)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, map[string]string{"version": version.V, "network": network, "name": n.Name, "url": at})
}

// prove marks url reached once a worker can find the harness there and get
// on the mesh: the pointer names url, url proves the harness key and passes
// the WebSocket upgrade the coordinator and the relay need. It gives up when the harness moves to another URL. A
// tunnel that starts again at the same URL proves it once.
func (a *machine) prove(ctx context.Context, url string) {
	if old, _ := a.proving.Swap(url).(string); old == url {
		return
	}
	for a.at() == url {
		pinned := ""
		if a.noPointer {
			pinned = url
		}
		if u, err := reach(ctx, pointer.Encode(a.k), pinned); err == nil && u == url {
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

func (a *machine) join(w http.ResponseWriter, r *http.Request) {
	n, err := LoadNetwork(a.dir)
	if err != nil {
		fail(w, err)
		return
	}
	join := ""
	if u := a.at(); !a.noPointer || u != "" { // none until the harness has a URL to pin
		join = n.Join(a.k, a.noPointer, u)
	}
	reply(w, map[string]string{"join": join})
}

func (a *machine) key(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if err := json.NewDecoder(io.LimitReader(r.Body, maxKey)).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" || strings.ContainsAny(in.Name, "\r\n") {
		fail(w, errors.New(`the body is {"name"}, a name of one line`))
		return
	}
	if a.noPointer && a.at() == "" {
		http.Error(w, "the harness has no URL yet for the key to carry: its tunnel is starting", http.StatusServiceUnavailable)
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
	token := auth.Token(auth.Key, pointer.Encode(a.k), secret)
	if a.noPointer {
		token = auth.Pin(token, a.at())
	}
	reply(w, map[string]string{"name": in.Name, "key": token})
}

func (a *machine) token(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if err := json.NewDecoder(io.LimitReader(r.Body, maxKey)).Decode(&in); err != nil {
		fail(w, errors.New(`the body is {"name"}, a worker name`))
		return
	}
	if err := coordinator.Name(in.Name); err != nil {
		fail(w, err)
		return
	}
	if a.noPointer && a.at() == "" {
		http.Error(w, "the harness has no URL yet for the token to carry: its tunnel is starting", http.StatusServiceUnavailable)
		return
	}
	secret, err := Auth(a.dir).Issue(auth.Join, in.Name)
	if errors.Is(err, auth.ErrTaken) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	token := auth.Token(auth.Join, pointer.Encode(a.k), secret)
	if a.noPointer {
		token = auth.Pin(token, a.at())
	}
	reply(w, map[string]string{"name": in.Name, "token": token})
}

// A key's row in GET /keys. Entries of one name, as reinstalls add default
// keys, are one row, as DELETE revokes them together.
type keyRow struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created,omitzero"`
	Used    time.Time `json:"used,omitzero"`
	Self    bool      `json:"self,omitempty"`
	Harness bool      `json:"harness,omitempty"` // the default key, the harness machine's own, which DELETE refuses
}

func (a *machine) keys(w http.ResponseWriter, r *http.Request) {
	t, err := Auth(a.dir).Read()
	if err != nil {
		fail(w, err)
		return
	}
	rows := []keyRow{}
	for _, e := range t[auth.Key] {
		i := slices.IndexFunc(rows, func(k keyRow) bool { return k.Name == e.Name })
		if i < 0 {
			rows, i = append(rows, keyRow{Name: e.Name, Created: e.Created, Harness: e.Name == DefaultKey}), len(rows)
		}
		k := &rows[i]
		k.Self = k.Self || e.Hash == caller(r)
		if e.Used.After(k.Used) {
			k.Used = e.Used
		}
	}
	reply(w, rows)
}

// revoke refuses the default key, which the harness's machine logs in with,
// and the key that asks: a device logs out instead.
func (a *machine) revoke(w http.ResponseWriter, r *http.Request) {
	name, store := r.PathValue("name"), Auth(a.dir)
	t, err := store.Read()
	if err != nil {
		fail(w, err)
		return
	}
	switch es := slices.DeleteFunc(slices.Clone(t[auth.Key]), func(e auth.Entry) bool { return e.Name != name }); {
	case len(es) == 0:
		http.Error(w, fmt.Sprintf("no key %q", name), http.StatusNotFound)
	case name == DefaultKey:
		http.Error(w, "the default key is the harness machine's own: revoke it there, with mainplane-server key revoke "+DefaultKey, http.StatusConflict)
	case slices.ContainsFunc(es, func(e auth.Entry) bool { return e.Hash == caller(r) }):
		http.Error(w, "this device is logged in with that key: log out instead", http.StatusConflict)
	default:
		if err := store.Revoke(auth.Key, name); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// caller is the hash of the api key a request carries.
func caller(r *http.Request) string {
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return auth.Hash(secret)
}

func (a *machine) drives(w http.ResponseWriter, r *http.Request) {
	c, err := loadConfig(a.path)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, orEmpty(c.Drives))
}

func (a *machine) setDrive(w http.ResponseWriter, r *http.Request) {
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
	var linked [][2]string
	if err := a.edit(func(c *Config) error {
		c.Drives[name] = d
		linked = append([][2]string{}, coordinator.Unlinked(c.Links, c.Drives)...)
		c.Links = append(c.Links, linked...)
		return nil
	}); err != nil {
		fail(w, err)
		return
	}
	reply(w, map[string][][2]string{"linked": linked})
}

func (a *machine) deleteDrive(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.edit(func(c *Config) error {
		if _, ok := c.Drives[name]; !ok {
			return fmt.Errorf("no drive %q", name)
		}
		delete(c.Drives, name)
		return nil
	}); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serves is why drive d, named name, cannot be served now, or nil: its
// server must be a connected worker that says it can serve drives.
func (a *machine) serves(name string, d coordinator.Drive) error {
	server, err := a.coord.Check(name, d)
	if err != nil {
		return err
	}
	ws := a.pool.List()
	i := slices.IndexFunc(ws, func(l api.Listed) bool { return l.Name == server && l.Refused == "" })
	switch {
	case i < 0:
		return fmt.Errorf("drive %s: its server %s is not connected", name, server)
	case !ws[i].DriveServer:
		return fmt.Errorf("drive %s: %s cannot serve drives: a drive server is Debian or Ubuntu, with no other NFS export and port 445 free", name, server)
	}
	return nil
}

// edit changes the config's drives and links with fn, saves the config and
// applies them. keyMu holds the read and write against any other config write.
func (a *machine) edit(fn func(*Config) error) error {
	keyMu.Lock()
	defer keyMu.Unlock()
	c, err := Load(a.path)
	if err != nil {
		return err
	}
	c.Drives = orEmpty(c.Drives)
	if err := fn(&c); err != nil {
		return err
	}
	if err := saveConfig(a.path, c); err != nil {
		return err
	}
	a.coord.Link(c.Links)
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
