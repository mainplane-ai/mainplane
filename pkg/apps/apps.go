// Package apps is what an agent may use beyond its workers. An app is a
// skill, instructions whose line 1 is their summary, and optionally an HTTP
// service the harness mounts at /apps/<name>. A built-in compiles in, a url
// app is a reverse proxy to a vendor's API, a text-only skill serves
// nothing, and a run app is a process the harness supervises. Code reaches
// <dir>/apps/ by the filesystem only: HTTP starts and stops apps and sets
// secrets, so a full api key never runs code of its choosing on the harness
// machine. Writable state is <dir>/app-data/<name>/, apart from the code, so
// a new version of an app keeps it.
package apps

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kinds, by what backs /apps/<name>.
const (
	Builtin = "builtin"
	Proxy   = "url"
	Skill   = "skill"
	Daemon  = "run"
)

// Statuses. A run app is supervised while it is enabled and has every secret
// it declares; the others are ready once their secrets are there.
const (
	Ready   = "ready"
	Running = "running"
	Stopped = "stopped"
	Missing = "missing_secrets"
)

// Entitlements an app may declare: the calls its app key may make.
const (
	Post = "post" // POST /agents/{id}/records, to agents holding the app, as app:<name>
	List = "list" // GET /agents, the agents holding the app
)

// Headers the harness sets on a call it forwards. A client's own X-Mainplane-*
// headers are dropped first, so an app can trust these.
const (
	AgentHeader   = "X-Mainplane-Agent"
	SessionHeader = "X-Mainplane-Session"
	ProxyHeader   = "X-Mainplane-Proxy-Secret"
)

// restartDelay spaces the launches of a run app that keeps exiting, so a
// broken one costs a line of log a second and not a busy loop.
const restartDelay = time.Second

// name is what a folder in apps/ must be called to be an app: a path
// segment and a word of a URL as it is.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Manifest is an app's app.json. At most one of URL and Run is set.
type Manifest struct {
	URL          string            `json:"url,omitempty"`     // reverse-proxied: /apps/<name>/{path} to <url>/{path}
	Run          []string          `json:"run,omitempty"`     // argv run in the app's folder, listening on 127.0.0.1:$PORT
	Secrets      []string          `json:"secrets,omitempty"` // names in .secrets this app receives, and no other
	Headers      map[string]string `json:"headers,omitempty"` // set on each proxied call, $SECRET expanded
	Entitlements []string          `json:"entitlements,omitempty"`
	Webhooks     bool              `json:"webhooks,omitempty"` // POST /webhooks/<name>/{path} reaches the app with no credential
}

// App is one app. A run app's process fields are under Registry.mu.
type App struct {
	Name  string
	Skill []byte
	Manifest
	dir     string       // a disk app's folder
	handler http.Handler // a built-in's

	disabled bool
	cmd      *exec.Cmd
	port     int
	secret   string
	exited   chan struct{}
}

func (a *App) Kind() string {
	switch {
	case a.handler != nil:
		return Builtin
	case a.URL != "":
		return Proxy
	case len(a.Run) > 0:
		return Daemon
	}
	return Skill
}

// Summary is line 1 of the skill, a heading's marks dropped.
func (a *App) Summary() string {
	line, _, _ := strings.Cut(string(a.Skill), "\n")
	return strings.TrimSpace(strings.TrimLeft(line, "# "))
}

// Info is an app as GET /apps shows it.
type Info struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Kind           string   `json:"kind"`
	Status         string   `json:"status"`
	Entitlements   []string `json:"entitlements"`
	MissingSecrets []string `json:"missing_secrets"`
}

// Registry is every app of one harness.
type Registry struct {
	Dir  string                  // the harness's
	URL  string                  // the harness API on loopback, for run apps
	Sign func(app string) string // an app key for app

	ctx  context.Context
	mu   sync.Mutex
	apps map[string]*App
}

func New(ctx context.Context, dir, url string, sign func(string) string) *Registry {
	return &Registry{Dir: dir, URL: url, Sign: sign, ctx: ctx, apps: map[string]*App{}}
}

// DataDir is app name's writable state, made on first use.
func (r *Registry) DataDir(name string) (string, error) {
	d := filepath.Join(r.Dir, "app-data", name)
	return d, os.MkdirAll(d, 0o700)
}

// Builtin adds a compiled-in app; serve gets the registry's context, which
// its work runs under, and its data dir.
func (r *Registry) Builtin(name string, skill []byte, serve func(ctx context.Context, dataDir string) (http.Handler, error)) error {
	d, err := r.DataDir(name)
	if err != nil {
		return err
	}
	h, err := serve(r.ctx, d)
	if err != nil {
		return fmt.Errorf("app %s: %w", name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apps[name] = &App{Name: name, Skill: skill, handler: h}
	return nil
}

// Load adds every app in <dir>/apps/; Up starts its run apps. A folder that
// is not an app is logged and left out.
func (r *Registry) Load() error {
	root := filepath.Join(r.Dir, "apps")
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		a, err := load(filepath.Join(root, e.Name()))
		if err != nil {
			log.Printf("app %s: %v", e.Name(), err)
			continue
		}
		r.mu.Lock()
		if _, ok := r.apps[a.Name]; ok {
			log.Printf("app %s: the name of a built-in, left out", a.Name)
		} else {
			r.apps[a.Name] = a
		}
		r.mu.Unlock()
	}
	return nil
}

// Up starts each run app that has its secrets, once the API it may call at
// $MAINPLANE_URL takes connections.
func (r *Registry) Up() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.apps {
		r.launch(a)
	}
}

func load(dir string) (*App, error) {
	a := &App{Name: filepath.Base(dir), dir: dir}
	if !name.MatchString(a.Name) {
		return nil, fmt.Errorf("an app's folder is named [a-z0-9-]")
	}
	var err error
	if a.Skill, err = os.ReadFile(filepath.Join(dir, "skill.md")); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "app.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &a.Manifest)
	}
	if err != nil {
		return nil, fmt.Errorf("app.json: %w", err)
	}
	if a.URL != "" && len(a.Run) > 0 {
		return nil, errors.New("app.json sets both url and run")
	}
	if u, err := url.Parse(a.URL); a.URL != "" && (err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "") {
		return nil, fmt.Errorf("app.json url %q is not http(s)://host", a.URL)
	}
	for k := range a.Headers {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Mainplane-") {
			return nil, fmt.Errorf("app.json header %s: X-Mainplane- headers are the harness's", k)
		}
	}
	for _, e := range a.Entitlements {
		if e != Post && e != List {
			return nil, fmt.Errorf("unknown entitlement %q; entitlements: %s, %s", e, Post, List)
		}
	}
	return a, nil
}

func (r *Registry) Get(name string) (*App, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.apps[name]
	return a, ok
}

// Names is every app's name, sorted.
func (r *Registry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(maps.Keys(r.apps))
}

func (r *Registry) List() []Info {
	secrets := r.secrets()
	out := []Info{}
	for _, n := range r.Names() {
		a, _ := r.Get(n)
		r.mu.Lock()
		out = append(out, Info{Name: n, Description: a.Summary(), Kind: a.Kind(), Status: r.status(a, secrets), Entitlements: append([]string{}, a.Entitlements...), MissingSecrets: missing(a, secrets)})
		r.mu.Unlock()
	}
	return out
}

// status is a's word, under mu.
func (r *Registry) status(a *App, secrets map[string]string) string {
	switch {
	case len(missing(a, secrets)) > 0:
		return Missing
	case a.Kind() != Daemon:
		return Ready
	case a.cmd != nil:
		return Running
	}
	return Stopped
}

func missing(a *App, secrets map[string]string) []string {
	out := []string{}
	for _, s := range a.Secrets {
		if _, ok := secrets[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// Serve forwards a call to the app: the path is the app's own, from /. The
// client's Authorization and X-Mainplane-* headers never reach it; agent and
// session, when set, are the harness's word on who calls.
func (r *Registry) Serve(w http.ResponseWriter, req *http.Request, name, path, agent, session string) {
	a, ok := r.Get(name)
	if !ok {
		http.Error(w, "no app "+name, http.StatusNotFound)
		return
	}
	req.Header.Del("Authorization")
	for k := range req.Header {
		if strings.HasPrefix(k, "X-Mainplane-") {
			req.Header.Del(k)
		}
	}
	if agent != "" {
		req.Header.Set(AgentHeader, agent)
		req.Header.Set(SessionHeader, session)
	}
	req.URL.Path, req.URL.RawPath = "/"+strings.TrimPrefix(path, "/"), ""
	secrets := r.secrets()
	if m := missing(a, secrets); len(m) > 0 {
		http.Error(w, fmt.Sprintf("app %s is missing secrets: %s; set them with PUT /secrets/{name}", name, strings.Join(m, ", ")), http.StatusServiceUnavailable)
		return
	}
	switch a.Kind() {
	case Builtin:
		a.handler.ServeHTTP(w, req)
	case Proxy:
		target, _ := url.Parse(a.URL) // load checked it
		proxy(target, func(h http.Header) {
			for k, v := range a.Headers {
				h.Set(k, os.Expand(v, func(s string) string {
					if slices.Contains(a.Secrets, s) {
						return secrets[s]
					}
					return ""
				}))
			}
		}).ServeHTTP(w, req)
	case Daemon:
		r.mu.Lock()
		port, secret, up := a.port, a.secret, a.cmd != nil
		r.mu.Unlock()
		if !up {
			http.Error(w, "app "+name+" is stopped", http.StatusServiceUnavailable)
			return
		}
		target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
		proxy(target, func(h http.Header) { h.Set(ProxyHeader, secret) }).ServeHTTP(w, req)
	default:
		http.Error(w, "app "+name+" is a text-only skill: read it at GET /skills/"+name, http.StatusNotFound)
	}
}

func proxy(target *url.URL, set func(http.Header)) http.Handler {
	return &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		set(pr.Out.Header)
	}}
}

// Webhook forwards an unauthenticated POST /webhooks/<name>/{path} to the
// app's /webhooks/{path}, with no agent: the app checks the sender's
// signature with a secret of its own.
func (r *Registry) Webhook(w http.ResponseWriter, req *http.Request, name, path string) {
	if a, ok := r.Get(name); !ok || !a.Webhooks {
		http.Error(w, "no webhooks for "+name, http.StatusNotFound)
		return
	}
	r.Serve(w, req, name, "/webhooks/"+path, "", "")
}

// Start enables a run app and launches it when its secrets are there.
func (r *Registry) Start(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.apps[name]
	if !ok || a.Kind() != Daemon {
		return fmt.Errorf("no run app %s", name)
	}
	a.disabled = false
	r.launch(a)
	return nil
}

// Stop disables a run app and waits for its process to end.
func (r *Registry) Stop(name string) error {
	r.mu.Lock()
	a, ok := r.apps[name]
	if !ok || a.Kind() != Daemon {
		r.mu.Unlock()
		return fmt.Errorf("no run app %s", name)
	}
	a.disabled = true
	cmd, exited := a.cmd, a.exited
	r.mu.Unlock()
	if cmd != nil {
		_ = cmd.Process.Kill()
		<-exited
	}
	return nil
}

// env is what a run app's process gets: enough of the harness's environment
// to find programs, and never the rest, which may hold provider keys.
var env = []string{"PATH", "HOME", "USER", "TMPDIR", "LANG", "SYSTEMROOT"}

// launch starts a run app that is enabled, has its secrets and is not
// running, under mu. The process is relaunched while it stays enabled.
func (r *Registry) launch(a *App) {
	secrets := r.secrets()
	if a.Kind() != Daemon || a.disabled || a.cmd != nil || len(missing(a, secrets)) > 0 || r.ctx.Err() != nil {
		return
	}
	port, err := freePort()
	if err != nil {
		log.Printf("app %s: %v", a.Name, err)
		return
	}
	data, err := r.DataDir(a.Name)
	if err != nil {
		log.Printf("app %s: %v", a.Name, err)
		return
	}
	cmd := exec.CommandContext(r.ctx, a.Run[0], a.Run[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = a.dir, log.Writer(), log.Writer()
	for _, k := range env {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	a.port, a.secret = port, rand.Text()
	cmd.Env = append(cmd.Env, "PORT="+strconv.Itoa(port), "DATA_DIR="+data, "MAINPLANE_PROXY_SECRET="+a.secret, "MAINPLANE_URL="+r.URL)
	if len(a.Entitlements) > 0 {
		cmd.Env = append(cmd.Env, "MAINPLANE_APP_KEY="+r.Sign(a.Name))
	}
	for _, s := range a.Secrets {
		cmd.Env = append(cmd.Env, s+"="+secrets[s])
	}
	if err := cmd.Start(); err != nil {
		log.Printf("app %s: %v", a.Name, err)
		return
	}
	log.Printf("app %s: started, pid %d, port %d", a.Name, cmd.Process.Pid, port)
	a.cmd, a.exited = cmd, make(chan struct{})
	go func(exited chan struct{}) {
		err := cmd.Wait()
		log.Printf("app %s: exited: %v", a.Name, err)
		r.mu.Lock()
		a.cmd = nil
		close(exited)
		r.mu.Unlock()
		time.AfterFunc(restartDelay, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.launch(a)
		})
	}(a.exited)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Close stops every run app; the registry's context ending does too.
func (r *Registry) Close() {
	for _, n := range r.Names() {
		if a, _ := r.Get(n); a.Kind() == Daemon {
			_ = r.Stop(n)
		}
	}
}
