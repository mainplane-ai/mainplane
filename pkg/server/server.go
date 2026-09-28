// Package server assembles the roles a mainplane-server process runs. Today
// that is one role, harness.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/tunnel"
)

// Config is the self-hosted config file. Provider values are expanded from
// the environment, so a key can be "$ANTHROPIC_API_KEY".
type Config struct {
	Admin     string              `json:"admin"` // directory holding sessions, the auth table and the tunnel
	HTTP      string              `json:"http"`  // loopback address the tunnel carries connectors and workers to
	Providers map[string]Provider `json:"providers"`
}

func (c Config) Auth() auth.Store { return auth.Store{Path: filepath.Join(c.Admin, "auth.json")} }

func (c Config) urlFile() string { return filepath.Join(c.Admin, "url") }

// URL is what every token carries: where its holder reaches this harness. The
// running harness writes it when its tunnel starts.
func (c Config) URL() (string, error) {
	b, err := os.ReadFile(c.urlFile())
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("the harness has no URL yet: it is not running, or its tunnel has not started")
	}
	return string(b), err
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

// Harness runs the harness role on one loopback port, behind a quick tunnel,
// until ctx ends or the listener or the tunnel fails: workers open a
// WebSocket at /worker with a join secret, connectors call every other route
// with an api key, every session that was open on start resumes.
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
	store := c.Auth()
	t, err := store.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pool := harness.NewPool(func(secret string) bool { return store.Check(auth.Join, secret) })
	h := &harness.Harness{Sessions: statefile.Sessions{Dir: c.Admin}, Providers: ps, Workers: pool}
	mux := http.NewServeMux()
	mux.Handle("/worker", pool)
	mux.Handle("/", store.Bearer(harness.Handler(ctx, h)))
	srv := &http.Server{Addr: c.HTTP, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := h.Resume(ctx); err != nil {
		return err
	}
	log.Printf("harness: http and workers on %s, sessions in %s, %d api keys, %d join secrets", c.HTTP, c.Admin, len(t[auth.Key]), len(t[auth.Join]))
	errs := make(chan error, 2)
	go func() { errs <- srv.ListenAndServe() }()
	go func() {
		errs <- tunnel.Quick(ctx, c.Admin, "http://"+c.HTTP, func(url string) {
			log.Printf("harness: reached at %s, a quick tunnel: the URL is temporary", url)
			if err := os.WriteFile(c.urlFile(), []byte(url), 0o644); err != nil {
				log.Printf("harness: %v", err)
			}
		})
	}()
	if err := <-errs; ctx.Err() == nil {
		return err
	}
	return nil
}
