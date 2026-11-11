// Package apps serves a harness's apps: what an agent may call from a worker
// with its session's key. An app is a folder with skill.md, the instructions
// an agent reads, and maybe app.json, which names the service behind it: a
// URL the harness proxies to with the app's secrets, or a process the
// harness runs. Three apps are compiled in. Secrets stay on the harness.
package apps

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/apps/message"
	"github.com/mainplane-ai/mainplane/pkg/apps/orchestrate"
	"github.com/mainplane-ai/mainplane/pkg/apps/schedule"
	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
)

// The headers an app trusts: the harness removes whatever a caller sent
// under the prefix and sets these itself.
const (
	prefix      = "X-Mainplane-"
	agentHeader = prefix + "Agent"
	sessHeader  = prefix + "Session"
	proxyHeader = prefix + "Proxy-Secret"
)

// A secret's value is a key or a PEM file, far shorter than this.
const maxSecret = 1 << 20

// An app's name is a folder and a path segment, a secret's an environment
// variable.
var (
	appName    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Spec is app.json. At most one of URL and Run is set; with neither the app
// is its skill alone.
type Spec struct {
	URL          string            `json:"url,omitempty"`          // calls are proxied to it
	Run          []string          `json:"run,omitempty"`          // the harness runs it in the app's folder and proxies calls to its $PORT
	Secrets      []string          `json:"secrets,omitempty"`      // the names in .secrets the app gets, and no others
	Headers      map[string]string `json:"headers,omitempty"`      // set on proxied calls, $SECRET replaced
	Entitlements []string          `json:"entitlements,omitempty"` // what the app's own key may ask of the harness: post, list
	Webhooks     bool              `json:"webhooks,omitempty"`     // POST /webhooks/<app>/ reaches the app with no key
}

// Info is an app as GET /apps shows it.
type Info struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Kind           string   `json:"kind"`   // built-in, proxy, skill, daemon
	Status         string   `json:"status"` // ready, running, stopped, missing_secrets
	Entitlements   []string `json:"entitlements"`
	MissingSecrets []string `json:"missing_secrets"`
}

type app struct {
	name, dir, skill string
	spec             Spec
	handler          http.Handler // a built-in's
}

func (a app) kind() string {
	switch {
	case a.handler != nil:
		return "built-in"
	case a.spec.URL != "":
		return "proxy"
	case len(a.spec.Run) > 0:
		return "daemon"
	}
	return "skill"
}

// Manager is the apps of one harness directory: apps/<name>/ holds an
// installed app, app-data/<name>/ what an app writes, .secrets the secrets.
type Manager struct {
	h   *harness.Harness
	dir string
	k   ed25519.PrivateKey
	key string // the harness key as tokens carry it
	url string // where a daemon reaches the harness
	ctx context.Context

	builtin map[string]app

	mu      sync.Mutex // daemons, stopped, and the secrets file
	daemons map[string]*daemon
	stopped map[string]bool // daemons an admin stopped, until a start or a restart of the harness
}

// New is the manager with the built-in apps in it, and starts every daemon
// app whose secrets are there. ctx ends the daemons. key is the harness key
// as a token names it, url the harness's own address for a daemon to call.
func New(ctx context.Context, h *harness.Harness, dir string, k ed25519.PrivateKey, key, url string) (*Manager, error) {
	m := &Manager{h: h, dir: dir, k: k, key: key, url: url, ctx: ctx, builtin: map[string]app{}, daemons: map[string]*daemon{}, stopped: map[string]bool{}}
	// The agents are read before the apps start: a timer due now needs its agent.
	if err := h.LoadAgents(); err != nil {
		return nil, err
	}
	sched, err := schedule.New(ctx, h, m.data("schedule"))
	if err != nil {
		return nil, err
	}
	orch, err := orchestrate.New(ctx, h, m.data("orchestrate"))
	if err != nil {
		return nil, err
	}
	m.builtin["schedule"] = app{name: "schedule", skill: schedule.Skill, handler: sched}
	m.builtin["message"] = app{name: "message", skill: message.Skill, handler: message.New(ctx, h)}
	m.builtin["orchestrate"] = app{name: "orchestrate", skill: orchestrate.Skill, handler: orch}
	h.Skill, h.Stopped = m.summary, orch.Stopped
	if err := os.MkdirAll(filepath.Join(dir, "apps"), 0o755); err != nil {
		return nil, err
	}
	return m, m.sync()
}

func (m *Manager) data(name string) string { return filepath.Join(m.dir, "app-data", name) }

// load reads the app as its folder has it now, so an app is installed or
// changed by its files alone. A built-in wins its name.
func (m *Manager) load(name string) (app, error) {
	if a, ok := m.builtin[name]; ok {
		return a, nil
	}
	a := app{name: name, dir: filepath.Join(m.dir, "apps", name)}
	if !appName.MatchString(name) {
		return a, fmt.Errorf("unknown app %q", name)
	}
	skill, err := os.ReadFile(filepath.Join(a.dir, "skill.md"))
	if err != nil {
		return a, fmt.Errorf("unknown app %q", name)
	}
	a.skill = string(skill)
	b, err := os.ReadFile(filepath.Join(a.dir, "app.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &a.spec)
	}
	if err == nil && a.spec.URL != "" && len(a.spec.Run) > 0 {
		err = errors.New("url and run are both set")
	}
	if err != nil {
		return a, fmt.Errorf("app %s: app.json: %w", name, err)
	}
	return a, nil
}

// names is every app the harness has, sorted.
func (m *Manager) names() []string {
	names := slices.Collect(maps.Keys(m.builtin))
	entries, _ := os.ReadDir(filepath.Join(m.dir, "apps"))
	for _, e := range entries {
		if _, err := m.load(e.Name()); err == nil && e.IsDir() && !slices.Contains(names, e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names
}

// summary is line 1 of the app's skill, "# <name>: <what it does>", without
// the name: what a listing says of it.
func (m *Manager) summary(name string) (string, bool) {
	a, err := m.load(name)
	line, _, _ := strings.Cut(a.skill, "\n")
	return strings.TrimPrefix(strings.TrimSpace(strings.TrimLeft(line, "# ")), name+": "), err == nil
}

func (m *Manager) info(name string) (Info, error) {
	a, err := m.load(name)
	if err != nil {
		return Info{}, err
	}
	secrets, err := m.secrets()
	if err != nil {
		return Info{}, err
	}
	i := Info{Name: name, Kind: a.kind(), Status: "ready", Entitlements: append([]string{}, a.spec.Entitlements...), MissingSecrets: missing(a.spec, secrets)}
	i.Description, _ = m.summary(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case len(i.MissingSecrets) > 0:
		i.Status = "missing_secrets"
	case i.Kind == "daemon" && m.daemons[name] == nil:
		i.Status = "stopped"
	case i.Kind == "daemon":
		i.Status = "running"
	}
	return i, nil
}

// secrets is the .secrets file: one JSON object of name to value.
func (m *Manager) secrets() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readSecrets()
}

func (m *Manager) readSecrets() (map[string]string, error) {
	s := map[string]string{}
	b, err := os.ReadFile(filepath.Join(m.dir, ".secrets"))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}

// setSecret writes the file whole with name set, or gone when value is nil,
// then restarts the daemons that take the secret so they run with it.
func (m *Manager) setSecret(name string, value *string) error {
	if !secretName.MatchString(name) {
		return fmt.Errorf("bad secret name %q: letters, digits and _", name)
	}
	m.mu.Lock()
	s, err := m.readSecrets()
	if err == nil {
		if delete(s, name); value != nil {
			s[name] = *value
		}
		var b []byte
		if b, err = json.MarshalIndent(s, "", "  "); err == nil {
			path := filepath.Join(m.dir, ".secrets")
			if err = os.WriteFile(path+".tmp", b, 0o600); err == nil {
				err = os.Rename(path+".tmp", path)
			}
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	for _, n := range m.names() {
		if a, err := m.load(n); err == nil && slices.Contains(a.spec.Secrets, name) {
			m.stop(n)
		}
	}
	return m.sync()
}

func missing(s Spec, secrets map[string]string) []string {
	out := []string{}
	for _, n := range s.Secrets {
		if _, ok := secrets[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// Routes adds what a full key may ask about apps: the skills, the apps and
// their daemons, and the secrets' names. No route reads a secret's value.
//
//	GET    /skills               -> [{"name","description"}]
//	GET    /skills/{app}         -> the app's skill.md
//	GET    /apps                 -> [Info]
//	POST   /apps/{name}/start    run a daemon app                    -> 204
//	POST   /apps/{name}/stop     end it until a start                -> 204
//	GET    /secrets              -> ["NAME"]
//	PUT    /secrets/{name}       the value as the body; starts the   -> 204
//	                             apps that waited for it
//	DELETE /secrets/{name}       -> 204
func (m *Manager) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /skills", func(w http.ResponseWriter, r *http.Request) { m.skills(w, m.names()) })
	mux.HandleFunc("GET /skills/{app}", func(w http.ResponseWriter, r *http.Request) { m.skill(w, r.PathValue("app")) })
	mux.HandleFunc("GET /apps", func(w http.ResponseWriter, r *http.Request) {
		out := []Info{}
		for _, n := range m.names() {
			i, err := m.info(n)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, i)
		}
		reply(w, out)
	})
	done := func(w http.ResponseWriter, err error) {
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("POST /apps/{name}/start", func(w http.ResponseWriter, r *http.Request) { done(w, m.enable(r.PathValue("name"), true)) })
	mux.HandleFunc("POST /apps/{name}/stop", func(w http.ResponseWriter, r *http.Request) { done(w, m.enable(r.PathValue("name"), false)) })
	mux.HandleFunc("GET /secrets", func(w http.ResponseWriter, r *http.Request) {
		s, err := m.secrets()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(w, append([]string{}, slices.Sorted(maps.Keys(s))...)) // a list, with no secrets too
	})
	mux.HandleFunc("PUT /secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSecret))
		v := string(b)
		if err == nil && strings.TrimSpace(v) == "" { // an empty one would count as there, and start its apps with nothing
			err = errors.New("a secret has a value: DELETE /secrets/<name> removes one")
		}
		if err == nil {
			err = m.setSecret(r.PathValue("name"), &v)
		}
		done(w, err)
	})
	mux.HandleFunc("DELETE /secrets/{name}", func(w http.ResponseWriter, r *http.Request) { done(w, m.setSecret(r.PathValue("name"), nil)) })
}

// Handler is the harness's front door. A session's key reaches the skills
// and apps of the agent whose live session it is, and nothing else. An
// app's key reaches what its entitlements name. A webhook needs no key.
// Every other request goes to full, which checks for a full key; routes is
// the same routes behind that check, for a call this door has let in.
func (m *Manager) Handler(full, routes http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case strings.HasPrefix(r.URL.Path, "/webhooks/"):
			m.webhook(w, r)
		case strings.HasPrefix(token, "mp_"+auth.Session+"_"):
			m.session(w, r, token)
		case strings.HasPrefix(token, "mp_"+auth.App+"_"):
			m.app(w, r, token, routes)
		default:
			full.ServeHTTP(w, r)
		}
	})
}

// session serves a call made with a session's key. The key holds while the
// session is its agent's live one and the agent has the app: a rollover, or
// taking the app away, refuses the next call with no table to update.
func (m *Manager) session(w http.ResponseWriter, r *http.Request, token string) {
	sid, ok := auth.Signed(m.k, auth.Session, token)
	if !ok {
		http.Error(w, "session key refused", http.StatusUnauthorized)
		return
	}
	a, ok := m.h.Holder(sid)
	if !ok {
		http.Error(w, "session "+sid+" is not an agent's live session: its key reaches no app", http.StatusForbidden)
		return
	}
	kind, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	name, path, sub := strings.Cut(rest, "/")
	skills := kind == "skills" && r.Method == http.MethodGet && !sub
	switch {
	case skills && name == "":
		m.skills(w, a.Apps)
	case !skills && kind != "apps" || name == "":
		http.Error(w, "a session key reaches GET /skills, GET /skills/<app> and /apps/<app>/ only", http.StatusForbidden)
	case !slices.Contains(a.Apps, name):
		http.Error(w, "agent "+a.ID+" does not have app "+name, http.StatusForbidden)
	case skills:
		m.skill(w, name)
	default:
		m.dispatch(w, r, name, "/"+path, a.ID, sid)
	}
}

// app serves a call an app makes with its own key: only what app.json's
// entitlements name, and only about agents that have the app.
func (m *Manager) app(w http.ResponseWriter, r *http.Request, token string, routes http.Handler) {
	name, ok := auth.Signed(m.k, auth.App, token)
	a, err := m.load(name)
	if !ok || err != nil {
		http.Error(w, "app key refused", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/agents" && slices.Contains(a.spec.Entitlements, "list"):
		type agent struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		out := []agent{}
		for _, i := range m.h.ListAgents(func(a harness.Agent) bool { return slices.Contains(a.Apps, name) }) {
			out = append(out, agent{i.ID, i.Name})
		}
		reply(w, out)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "agents" && parts[2] == "records" && slices.Contains(a.spec.Entitlements, "post"):
		if t, err := m.h.Agent(parts[1]); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		} else if !slices.Contains(t.Apps, name) {
			http.Error(w, "agent "+t.ID+" does not have app "+name, http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		q.Set("via", "app:"+name) // the app does not choose who it is
		r.URL.RawQuery = q.Encode()
		routes.ServeHTTP(w, r)
	default:
		http.Error(w, "app "+name+" has no entitlement for this", http.StatusForbidden)
	}
}

// webhook lets an outside service reach an app that asked for it. No agent
// is named: the app checks the sender by its own secret.
func (m *Manager) webhook(w http.ResponseWriter, r *http.Request) {
	name, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/webhooks/"), "/")
	if a, err := m.load(name); err != nil || !a.spec.Webhooks || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	m.dispatch(w, r, name, "/webhooks/"+path, "", "")
}

// dispatch hands the call to the app as path, with the caller's own claims
// of who it is removed and, for an agent's call, the harness's word put in.
func (m *Manager) dispatch(w http.ResponseWriter, r *http.Request, name, path, agent, session string) {
	a, err := m.load(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	secrets, err := m.secrets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if miss := missing(a.spec, secrets); len(miss) > 0 {
		http.Error(w, "app "+name+" is missing secrets: "+strings.Join(miss, ", ")+"; an admin sets one with PUT /secrets/<name>", http.StatusServiceUnavailable)
		return
	}
	r = r.Clone(r.Context())
	for k := range r.Header {
		if strings.HasPrefix(k, prefix) {
			r.Header.Del(k)
		}
	}
	// A webhook keeps its Authorization: it is the sender's word to the app,
	// and where some senders put the signature the app checks. An agent's
	// is the session's key, which no app sees.
	if agent != "" {
		r.Header.Del("Authorization")
		r.Header.Set(agentHeader, agent)
		r.Header.Set(sessHeader, session)
	}
	r.URL.Path, r.URL.RawPath = path, ""
	switch a.kind() {
	case "built-in":
		a.handler.ServeHTTP(w, r)
	case "proxy":
		set := map[string]string{}
		for k, v := range a.spec.Headers {
			set[k] = os.Expand(v, func(n string) string {
				if slices.Contains(a.spec.Secrets, n) {
					return secrets[n]
				}
				return ""
			})
		}
		forward(w, r, a.spec.URL, set)
	case "daemon":
		port, secret, err := m.running(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		forward(w, r, fmt.Sprintf("http://127.0.0.1:%d", port), map[string]string{proxyHeader: secret})
	default:
		http.Error(w, "app "+name+" is a skill with no service: read it at /skills/"+name, http.StatusNotFound)
	}
}

// forward proxies r to base plus r's path, with set on top of its headers.
func forward(w http.ResponseWriter, r *http.Request, base string, set map[string]string) {
	u, err := url.Parse(base)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	(&httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(u)
		for k, v := range set {
			p.Out.Header.Set(k, v)
		}
	}}).ServeHTTP(w, r)
}

func (m *Manager) skills(w http.ResponseWriter, names []string) {
	type skill struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := []skill{}
	for _, n := range names {
		if line, ok := m.summary(n); ok {
			out = append(out, skill{n, line})
		}
	}
	reply(w, out)
}

func (m *Manager) skill(w http.ResponseWriter, name string) {
	a, err := m.load(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = io.WriteString(w, a.skill)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
