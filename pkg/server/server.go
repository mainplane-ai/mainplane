// Package server assembles the roles a mainplane-server process runs. Today
// that is one role, harness.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Config is the self-hosted config file. Provider values are expanded from
// the environment, so a key can be "$ANTHROPIC_API_KEY".
type Config struct {
	Admin     string              `json:"admin"`   // directory holding sessions and the auth table
	Host      string              `json:"host"`    // the name this box is reached by; tokens carry it
	Workers   string              `json:"workers"` // address workers dial
	HTTP      string              `json:"http"`    // address connectors call
	Providers map[string]Provider `json:"providers"`
}

func (c Config) Auth() auth.Store { return auth.Store{Path: filepath.Join(c.Admin, "auth.json")} }

// Address is what a token of kind carries: where its holder reaches this harness.
func (c Config) Address(kind string) (string, error) {
	_, port, err := net.SplitHostPort(map[string]string{auth.Key: c.HTTP, auth.Join: c.Workers}[kind])
	if err != nil {
		return "", err
	}
	addr := net.JoinHostPort(c.Host, port)
	if kind == auth.Key {
		return "http://" + addr, nil
	}
	return addr, nil
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

// Harness runs the harness role until ctx ends or one listener fails: workers
// dial in with a join secret, connectors call over HTTP with an api key,
// every session that was open on start resumes. Whichever ends first ends
// the other.
func Harness(ctx context.Context, c Config) error {
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
	srv := &http.Server{Addr: c.HTTP, Handler: store.Bearer(harness.Handler(ctx, h))}
	errs := make(chan error, 2)
	go func() { errs <- h.Workers.Listen(ctx, c.Workers) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := h.Resume(ctx); err != nil {
		return err
	}
	log.Printf("harness: workers on %s, http on %s, sessions in %s, %d api keys, %d join secrets", c.Workers, c.HTTP, c.Admin, len(t[auth.Key]), len(t[auth.Join]))
	go func() { errs <- srv.ListenAndServe() }()
	if err := <-errs; ctx.Err() == nil {
		return err
	}
	return nil
}
