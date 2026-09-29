// Package server assembles the roles a mainplane-server process runs. Today
// that is one role, harness.
package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

// Config is the self-hosted config file. Provider values are expanded from
// the environment, so a key can be "$ANTHROPIC_API_KEY".
type Config struct {
	Admin     string              `json:"admin"`            // directory holding sessions, the auth table and the tunnel
	HTTP      string              `json:"http"`             // loopback address the tunnel carries connectors and workers to
	Tunnel    *Tunnel             `json:"tunnel,omitempty"` // the user's own; none is a quick tunnel
	Providers map[string]Provider `json:"providers"`
}

// Tunnel is a tunnel the user made in Cloudflare, which routes URL to it and
// it to HTTP.
type Tunnel struct {
	URL   string `json:"url"`
	Token string `json:"token"` // the tunnel's, as Cloudflare shows it
}

func (c Config) Auth() auth.Store { return auth.Store{Path: filepath.Join(c.Admin, "auth.json")} }

// Key is the harness key every token carries, made on first use.
func (c Config) Key() (ed25519.PrivateKey, error) {
	return pointer.Key(filepath.Join(c.Admin, "harness.key"))
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
		key, url, region := os.ExpandEnv(p.Key), os.ExpandEnv(p.URL), os.ExpandEnv(p.Region)
		switch name {
		case "anthropic":
			out[name] = provider.Anthropic(key)
		case "openai":
			out[name] = provider.OpenAI(key)
		case "openai-chat":
			out[name] = provider.OpenAIChat(url, key)
		case "gemini":
			out[name] = provider.Gemini(key)
		case "bedrock":
			out[name] = provider.Bedrock(region, key)
		default:
			return nil, fmt.Errorf("unknown provider %q", name)
		}
	}
	return out, nil
}

// Harness runs the harness role on one loopback port, behind the user's own
// tunnel or a quick one, until ctx ends or the listener, the mesh node or the
// tunnel fails: /id proves the harness key to anyone before they send a
// secret, /ts2021 coordinates the mesh, /derp relays between its nodes,
// connectors call every other route with an api key, every session that was
// open on start resumes. The harness joins its own mesh, and workers dial it
// there. The pointer and the relay map follow the tunnel's URL.
func Harness(ctx context.Context, c Config) error {
	if host, _, _ := net.SplitHostPort(c.HTTP); !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("http %q: the harness listens on loopback only, such as 127.0.0.1:8080; the tunnel is the one way in", c.HTTP)
	}
	ps, err := providers(c.Providers)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Admin, 0o755); err != nil {
		return err
	}
	k, err := c.Key()
	if err != nil {
		return err
	}
	store := c.Auth()
	t, err := store.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limit := &auth.Limit{}
	join := func(secret, addr string) (bool, error) {
		if err := limit.Wait(addr); err != nil {
			return false, err
		}
		e, ok := store.Find(auth.Join, secret)
		if !ok {
			limit.Refused(addr)
			return false, errors.New("join secret refused")
		}
		return e.Ephemeral, nil
	}
	pool := harness.NewPool()
	coord, err := coordinator.New(c.Admin, join)
	if err != nil {
		return err
	}
	h := &harness.Harness{Sessions: statefile.Sessions{Dir: c.Admin}, Providers: ps, Workers: pool, Remove: coord.Remove}
	var at atomic.Value
	at.Store("")
	urls := make(chan string, 1)
	go publish(ctx, k, urls)
	mux := http.NewServeMux()
	mux.Handle("/id", pointer.ID(k, func() string { return at.Load().(string) }, coord.Public().String()))
	coord.Handle(mux)
	derp := relay.New()
	derp.SetVerifyClientFunc(coord.Known)
	relay.Handle(mux, derp)
	mux.Handle("/", store.Bearer(limit, harness.Handler(ctx, h)))
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
	log.Printf("harness: http on %s, sessions in %s, %d api keys, %d join secrets", c.HTTP, c.Admin, len(t[auth.Key]), len(t[auth.Join]))
	errs := make(chan error, 3)
	go func() { errs <- srv.Serve(ln) }()
	go func() {
		l, err := coord.Listen(ctx, filepath.Join(c.Admin, "mesh"), "http://"+c.HTTP, worker.Port)
		if err == nil {
			err = pool.Serve(l, coord.Node)
		}
		errs <- err
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
			errs <- tunnel.Own(ctx, c.Admin, c.Tunnel.URL, c.Tunnel.Token, "http://"+c.HTTP, up)
		} else {
			errs <- tunnel.Quick(ctx, c.Admin, "http://"+c.HTTP, up)
		}
	}()
	if err := <-errs; ctx.Err() == nil {
		return err
	}
	return nil
}

// publish puts each URL from urls on the pointer, again daily so the record
// does not expire, and a minute after a failure.
func publish(ctx context.Context, k ed25519.PrivateKey, urls <-chan string) {
	url, wait := "", republish
	for {
		select {
		case <-ctx.Done():
			return
		case url = <-urls:
		case <-time.After(wait):
		}
		wait = republish
		if err := pointer.Publish(ctx, k, url); err != nil {
			log.Printf("harness: %v; again in %s", err, publishRetry)
			wait = publishRetry
		}
	}
}
