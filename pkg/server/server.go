// Package server assembles the roles a mainplane-server process runs. Today
// that is one role, harness.
package server

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/coordinator"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/relay"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/tunnel"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// The pointer drops a record 30 days after its last publish; a daily one
// keeps it while the URL holds. A failed publish is the pointer or the
// network down, and a minute is soon enough for workers that wait on it.
const (
	republish    = 24 * time.Hour
	publishRetry = time.Minute
)

// A person who saves the config sees it applied within this.
const reread = 2 * time.Second

// A provider key is one line, far shorter than this.
const maxKey = 4 << 10

// Config is the self-hosted config file. Provider values are expanded from
// the environment, so a key can be "$ANTHROPIC_API_KEY". The directory it is
// in is the harness's: the harness key, the auth table, the mesh and the
// tunnel are there, and sessions in sessions/ under it, the one directory a
// drive may ever share.
type Config struct {
	HTTP      string                       `json:"http"`             // loopback address the tunnel carries connectors and workers to
	Tunnel    *Tunnel                      `json:"tunnel,omitempty"` // the user's own; none is a quick tunnel
	Providers map[string]Provider          `json:"providers"`
	Links     [][2]string                  `json:"links,omitempty"`  // pairs of workers, by name, that reach each other on the mesh
	Drives    map[string]coordinator.Drive `json:"drives,omitempty"` // by name: each one's server and workers
}

// Tunnel is a tunnel the user made in Cloudflare, which routes URL to it and
// it to HTTP.
type Tunnel struct {
	URL   string `json:"url"`
	Token string `json:"token"` // the tunnel's, as Cloudflare shows it
}

// Auth is the auth table of the harness in dir.
func Auth(dir string) auth.Store { return auth.Store{Path: filepath.Join(dir, "auth.json")} }

// Key is the harness key every token carries, made on first use.
func Key(dir string) (ed25519.PrivateKey, error) {
	return pointer.Key(filepath.Join(dir, "harness.key"))
}

type Provider struct {
	Key    string `json:"key"`
	URL    string `json:"url,omitempty"`    // openai-chat
	Region string `json:"region,omitempty"` // bedrock
}

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func providers(cfg map[string]Provider) (map[string]provider.Provider, error) {
	out := map[string]provider.Provider{}
	for name, p := range cfg {
		build, ok := provider.Supported[name]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q; supported: %s", name, strings.Join(slices.Sorted(maps.Keys(provider.Supported)), ", "))
		}
		out[name] = build(os.ExpandEnv(p.Key), os.ExpandEnv(p.URL), os.ExpandEnv(p.Region))
	}
	return out, nil
}

// Harness runs the harness role on one loopback port, behind the user's own
// tunnel or a quick one, until ctx ends or the listener, the mesh node or the
// tunnel fails: /id proves the harness key to anyone before they send a
// secret, /ts2021 coordinates the mesh, /derp relays between its nodes,
// connectors call every other route with an api key, every session that was
// open on start resumes. The harness joins its own mesh, and workers dial it
// there. The pointer and the relay map follow the tunnel's URL. The config
// at path is read again when it changes: providers, links and drives apply
// at once.
func Harness(ctx context.Context, path string) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	if host, _, _ := net.SplitHostPort(c.HTTP); !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("http %q: the harness listens on loopback only, such as 127.0.0.1:8080; the tunnel is the one way in", c.HTTP)
	}
	ps, err := providers(c.Providers)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		return err
	}
	k, err := Key(dir)
	if err != nil {
		return err
	}
	if err := network(dir, k); err != nil {
		return err
	}
	store := Auth(dir)
	t, err := store.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limit := &auth.Limit{}
	code := func() (string, error) {
		n, err := LoadNetwork(dir)
		return n.Code, err
	}
	join := func(secret, addr string) (string, error) {
		if err := limit.Wait(addr); err != nil {
			return "", err
		}
		c, err := code()
		if err != nil {
			return "", err
		}
		switch {
		case hmac.Equal([]byte(secret), []byte(auth.Secret(k, worker.Admin))):
			return worker.Admin, nil
		case hmac.Equal([]byte(secret), []byte(auth.Secret(k, c))):
			return "", nil
		}
		limit.Refused(addr)
		return "", errors.New("join secret refused: install again with the network name and the device code")
	}
	coord, err := coordinator.New(dir, k, join)
	if err != nil {
		return err
	}
	pool := harness.NewPool(coord.Desired)
	coord.Link(c.Links)
	setDrives(coord, pool, c.Drives)
	h := &harness.Harness{Sessions: statefile.Sessions{Dir: sessions}, Providers: ps, Workers: pool, Remove: coord.Remove}
	go watch(ctx, path, func(c Config) error {
		ps, err := providers(c.Providers)
		if err == nil {
			h.SetProviders(ps)
			coord.Link(c.Links)
			setDrives(coord, pool, c.Drives)
		}
		return err
	})
	var at atomic.Value
	at.Store("")
	urls := make(chan string, 1)
	go publish(ctx, dir, k, urls)
	mux := http.NewServeMux()
	mux.Handle("/id", pointer.ID(k, func() string { return at.Load().(string) }, coord.Public().String()))
	mux.Handle("POST /join", auth.JoinHandler(k, pointer.Encode(k), code, limit))
	coord.Handle(mux)
	derp := relay.New()
	derp.SetVerifyClientFunc(coord.Known)
	relay.Handle(mux, derp)
	routes := http.NewServeMux()
	routes.Handle("/", harness.Handler(ctx, h))
	routes.HandleFunc("PUT /providers/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := setKey(path, r.PathValue("name"), r.Body, h.SetProviders); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	api := store.Bearer(limit, routes)
	mux.Handle("/", api)
	srv := &http.Server{Addr: c.HTTP, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = derp.Close()
	}()
	if err := h.Resume(ctx); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.HTTP)
	if err != nil {
		return err
	}
	log.Printf("harness: http on %s, sessions in %s, %d api keys", c.HTTP, sessions, len(t[auth.Key]))
	errs := make(chan error, 4)
	go func() { errs <- srv.Serve(ln) }()
	go func() {
		ls, err := coord.Listen(ctx, filepath.Join(dir, "mesh"), "http://"+c.HTTP, worker.Port, worker.APIPort)
		if err != nil {
			errs <- err
			return
		}
		// The CLI on a worker, over the mesh: no tunnel, so no CF-Connecting-IP
		// to trust; the limit counts the node's address.
		go func() {
			errs <- http.Serve(ls[1], http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Header.Del("CF-Connecting-IP")
				api.ServeHTTP(w, r)
			}))
		}()
		errs <- pool.Serve(ls[0], coord.Node)
	}()
	up := func(url string) {
		log.Printf("harness: reached at %s", url)
		at.Store(url)
		coord.Relay(url)
		select {
		case <-urls:
		default:
		}
		urls <- url
	}
	go func() {
		if c.Tunnel != nil {
			errs <- tunnel.Own(ctx, dir, c.Tunnel.URL, c.Tunnel.Token, "http://"+c.HTTP, up)
		} else {
			errs <- tunnel.Quick(ctx, dir, "http://"+c.HTTP, up)
		}
	}()
	if err := <-errs; ctx.Err() == nil {
		return err
	}
	return nil
}

// keyMu keeps two keys set at once from writing over each other.
var keyMu sync.Mutex

// setKey makes the body the key of provider name in the config at path,
// beside the provider's other fields, and serves the providers it makes at
// once: the README runs mainplane new right after mainplane key, sooner than
// the watch rereads. The lock holds the write and serve in one order.
func setKey(path, name string, body io.Reader, serve func(map[string]provider.Provider)) error {
	b, err := io.ReadAll(io.LimitReader(body, maxKey+1))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(b))
	if key == "" || len(b) > maxKey || strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("a key is one line of 1 to %d bytes", maxKey)
	}
	keyMu.Lock()
	defer keyMu.Unlock()
	c, err := Load(path)
	if err != nil {
		return err
	}
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	p := c.Providers[name]
	p.Key = key
	c.Providers[name] = p
	ps, err := providers(c.Providers)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	serve(ps)
	return nil
}

// setDrives applies the drives and sends each worker its own. A drive that
// cannot be served is a config error in the log; the others are served.
func setDrives(coord *coordinator.Coordinator, pool *harness.Pool, ds map[string]coordinator.Drive) {
	for _, err := range coord.SetDrives(ds) {
		log.Printf("harness: config: %v", err)
	}
	pool.Resend()
}

// publish puts each URL from urls on the pointer, with the network name in
// dir, again daily so neither expires, and a minute after a failure.
func publish(ctx context.Context, dir string, k ed25519.PrivateKey, urls <-chan string) {
	url, wait := "", republish
	for {
		select {
		case <-ctx.Done():
			return
		case url = <-urls:
		case <-time.After(wait):
		}
		wait = republish
		n, err := LoadNetwork(dir)
		if err == nil {
			err = pointer.Claim(ctx, k, n.Name)
		}
		if err == nil {
			err = pointer.Publish(ctx, k, url)
		}
		if err != nil {
			log.Printf("harness: %v; again in %s", err, publishRetry)
			wait = publishRetry
		}
	}
}

// watch applies the config at path each time it changes, and logs one it
// cannot apply: the harness keeps what it had.
func watch(ctx context.Context, path string, apply func(Config) error) {
	var mod time.Time
	if fi, err := os.Stat(path); err == nil {
		mod = fi.ModTime()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(reread):
		}
		fi, err := os.Stat(path)
		if err != nil || fi.ModTime().Equal(mod) {
			continue
		}
		mod = fi.ModTime()
		c, err := Load(path)
		if err == nil {
			err = apply(c)
		}
		if err != nil {
			log.Printf("harness: %s: %v; the config before it holds", path, err)
			continue
		}
		log.Printf("harness: %s applied: %d providers, %d links, %d drives; any other change applies at the next start", path, len(c.Providers), len(c.Links), len(c.Drives))
	}
}
