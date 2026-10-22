// Package server assembles the roles a mainplane-server process runs. Today
// that is one role, harness.
package server

import (
	"cmp"
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

	"golang.org/x/net/http/httpguts"

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

// A provider body, a key and a URL, is far shorter than this.
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

// Provider is one the config names. Its name is free, and the catalog's ids
// are the names connectors know; its shape picks the code.
type Provider struct {
	Shape   string `json:"shape"`
	Key     string `json:"key,omitempty"`     // none for a shape that logs in
	URL     string `json:"url,omitempty"`     // the base url its SDK takes; none is the vendor's own
	Region  string `json:"region,omitempty"`  // bedrock
	Session string `json:"session,omitempty"` // the header the session's routing key goes in, for a server that routes its cache on one
}

// providerDir is the directory of provider name's own state, such as a
// login's tokens, in the harness's dir.
func providerDir(dir, name string) string { return filepath.Join(dir, "providers", name) }

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	c.named()
	return c, err
}

// named gives a provider with no shape its name as its shape, as each had in
// v0.5.3, so a config from then still loads.
func (c Config) named() {
	for name, p := range c.Providers {
		p.Shape = cmp.Or(p.Shape, name)
		c.Providers[name] = p
	}
}

// providers builds the config's providers. Each has a directory of its own
// under dir, where a login keeps what it needs.
func providers(dir string, cfg map[string]Provider) (map[string]provider.Provider, error) {
	out := map[string]provider.Provider{}
	for name, p := range cfg {
		build, ok := provider.Shapes[p.Shape]
		if !ok {
			return nil, fmt.Errorf("provider %s: unknown shape %q; shapes: %s", name, p.Shape, strings.Join(slices.Sorted(maps.Keys(provider.Shapes)), ", "))
		}
		if p.Shape == "bedrock" && os.ExpandEnv(p.Region) == "" && os.ExpandEnv(p.URL) == "" {
			return nil, fmt.Errorf("provider %s: a bedrock provider needs region or url", name)
		}
		if p.Session != "" && !httpguts.ValidHeaderFieldName(p.Session) {
			return nil, fmt.Errorf("provider %s: session %q is not a header name", name, p.Session)
		}
		pp := build(provider.Config{Key: os.ExpandEnv(p.Key), URL: os.ExpandEnv(p.URL), Region: os.ExpandEnv(p.Region), Dir: providerDir(dir, name)})
		pp.Session = cmp.Or(p.Session, pp.Session)
		out[name] = pp
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
	dir := filepath.Dir(path)
	ps, err := providers(dir, c.Providers)
	if err != nil {
		return err
	}
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
		case hmac.Equal([]byte(secret), []byte(adminSecret(k, c))):
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
	coord.Gone = pool.Drop
	coord.Link(c.Links)
	setDrives(coord, pool, c.Drives)
	h := &harness.Harness{Sessions: statefile.Sessions{Dir: sessions}, Providers: ps, Workers: pool, Remove: coord.Remove}
	go watch(ctx, path, func(c Config) error {
		ps, err := providers(dir, c.Providers)
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
		if err := setProvider(path, r.PathValue("name"), r.Body, h.SetProviders); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	logins := &logins{ctx: ctx, path: path, serve: h.SetProviders, m: map[string]*login{}}
	routes.HandleFunc("POST /providers/{name}/login", logins.start)
	routes.HandleFunc("POST /providers/{name}/login/code", logins.paste)
	routes.HandleFunc("GET /providers/{name}/login", logins.get)
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
		var err error
		if c.Tunnel != nil {
			err = tunnel.Own(ctx, dir, c.Tunnel.URL, c.Tunnel.Token, "http://"+c.HTTP, up)
		} else {
			err = tunnel.Quick(ctx, dir, "http://"+c.HTTP, up)
		}
		// for Reached, which a service's log does not reach
		if ctx.Err() == nil {
			_ = os.WriteFile(filepath.Join(dir, tunnelFailed), []byte(err.Error()), 0o600)
		}
		errs <- err
	}()
	if err := <-errs; ctx.Err() == nil {
		return err
	}
	return nil
}

// keyMu keeps two providers set at once from writing over each other.
var keyMu sync.Mutex

// setProvider makes the body, a Provider, the provider name in the config at
// path: the fields it sets replace the ones there, so a key alone renews the
// key of a provider the config has. It serves the providers it makes at
// once: the README runs mainplane new right after mainplane key, sooner than
// the watch rereads.
func setProvider(path, name string, body io.Reader, serve func(map[string]provider.Provider)) error {
	b, err := io.ReadAll(io.LimitReader(body, maxKey+1))
	if err != nil {
		return err
	}
	var in Provider
	if len(b) > maxKey || json.Unmarshal(b, &in) != nil {
		return fmt.Errorf("the body is a provider of at most %d bytes: {\"shape\",\"key\",\"url\",\"region\",\"session\"}", maxKey)
	}
	in.Key = strings.TrimSpace(in.Key)
	if in.Key == "" || strings.ContainsAny(in.Key, "\r\n") {
		return errors.New("a key is one line")
	}
	return saveProvider(path, name, in, serve)
}

// saveProvider writes in over the provider name in the config at path, each
// field in sets, and serves the providers. The lock holds the write and serve
// in one order.
func saveProvider(path, name string, in Provider, serve func(map[string]provider.Provider)) error {
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
	for _, f := range [][2]*string{{&p.Shape, &in.Shape}, {&p.Key, &in.Key}, {&p.URL, &in.URL}, {&p.Region, &in.Region}, {&p.Session, &in.Session}} {
		if *f[1] != "" {
			*f[0] = *f[1]
		}
	}
	c.Providers[name] = p
	ps, err := providers(filepath.Dir(path), c.Providers)
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
