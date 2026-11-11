package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/apps/message"
	"github.com/mainplane-ai/mainplane/pkg/apps/orchestrate"
	"github.com/mainplane-ai/mainplane/pkg/apps/schedule"
	"github.com/mainplane-ai/mainplane/pkg/auth"
)

// maxSecret bounds a secret value uploaded via PUT /secrets/{name}.
const maxSecret = 64 << 10

const (
	KindBuiltin = "builtin"
	KindProxy   = "proxy"
	KindSkill   = "skill"
	KindDaemon  = "daemon"
)

// Manifest is app.json. At most one of URL or Run is set.
type Manifest struct {
	URL          string            `json:"url,omitempty"`
	Run          []string          `json:"run,omitempty"`
	Secrets      []string          `json:"secrets,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Entitlements []string          `json:"entitlements,omitempty"`
	Webhooks     bool              `json:"webhooks,omitempty"`
}

// SkillEntry is one element of GET /skills.
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AppEntry is one element of GET /apps.
type AppEntry struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Kind           string   `json:"kind"`
	Status         string   `json:"status"`
	Entitlements   []string `json:"entitlements"`
	MissingSecrets []string `json:"missing_secrets"`
}

type appDef struct {
	name     string
	kind     string
	skill    string
	desc     string
	manifest Manifest
	dir      string
	dataDir  string
	handler  http.Handler
}

// Config configures a Manager.
type Config struct {
	Dir         string
	Host        Host
	LoopbackURL func() string
	AppToken    func(app string) string
}

// Manager owns .secrets, built-in apps, disk apps, and daemon supervisors.
type Manager struct {
	dir      string
	host     Host
	loopback func() string
	appToken func(string) string

	Secrets  *Secrets
	builtins map[string]appDef
	sched    *schedule.App
	orch     *orchestrate.App

	mu      sync.Mutex
	ctx     context.Context
	daemons map[string]*daemon
}

func New(cfg Config) (*Manager, error) {
	m := &Manager{
		dir:      cfg.Dir,
		host:     cfg.Host,
		loopback: cfg.LoopbackURL,
		appToken: cfg.AppToken,
		builtins: map[string]appDef{},
		daemons:  map[string]*daemon{},
		ctx:      context.Background(),
	}
	m.Secrets = newSecrets(filepath.Join(cfg.Dir, ".secrets"), m.reconcileDaemons)

	schedDir := m.dataDir("schedule")
	sched, err := schedule.New(schedDir, cfg.Host)
	if err != nil {
		return nil, err
	}
	m.sched = sched
	m.addBuiltin("schedule", schedule.Skill, schedule.ManifestJSON, sched)

	msg := message.New(cfg.Host)
	m.addBuiltin("message", message.Skill, message.ManifestJSON, msg)

	orchDir := m.dataDir("orchestrate")
	orch, err := orchestrate.New(orchDir, cfg.Host)
	if err != nil {
		return nil, err
	}
	m.orch = orch
	m.addBuiltin("orchestrate", orchestrate.Skill, orchestrate.ManifestJSON, orch)
	return m, nil
}

func (m *Manager) addBuiltin(name, skill string, raw []byte, h http.Handler) {
	var man Manifest
	_ = json.Unmarshal(raw, &man)
	m.builtins[name] = appDef{
		name:     name,
		kind:     KindBuiltin,
		skill:    skill,
		desc:     firstLine(skill),
		manifest: man,
		dataDir:  m.dataDir(name),
		handler:  h,
	}
}

func (m *Manager) dataDir(name string) string {
	return filepath.Join(m.dir, "app-data", name)
}

// Start runs the schedule loop and starts enabled daemon apps whose secrets exist.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	go m.sched.Start(ctx)
	m.reconcileDaemons()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		ds := slices.Collect(maps.Values(m.daemons))
		m.mu.Unlock()
		for _, d := range ds {
			d.stop()
		}
	}()
}

// NotifyAgentStop forwards terminal agent stops to orchestrate's notify_on_stop table.
func (m *Manager) NotifyAgentStop(ctx context.Context, info AgentInfo) {
	if m.orch != nil {
		m.orch.OnAgentStop(ctx, info)
	}
}

// ValidAppName reports whether name is a safe single-segment app identifier.
func ValidAppName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		ok := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || i > 0 && (c == '-' || c == '_')
		if !ok {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return line
		}
	}
	return ""
}

func (m *Manager) loadDiskApp(name string) (appDef, bool) {
	if !ValidAppName(name) {
		return appDef{}, false
	}
	dir := filepath.Join(m.dir, "apps", name)
	skillBytes, err := os.ReadFile(filepath.Join(dir, "skill.md"))
	var man Manifest
	manBytes, merr := os.ReadFile(filepath.Join(dir, "app.json"))
	if errors.Is(err, fs.ErrNotExist) && errors.Is(merr, fs.ErrNotExist) {
		return appDef{}, false
	}
	if merr == nil {
		if json.Unmarshal(manBytes, &man) != nil || man.URL != "" && len(man.Run) > 0 {
			return appDef{}, false
		}
	}
	kind := KindSkill
	switch {
	case len(man.Run) > 0:
		kind = KindDaemon
	case man.URL != "":
		kind = KindProxy
	}
	skill := string(skillBytes)
	return appDef{
		name:     name,
		kind:     kind,
		skill:    skill,
		desc:     firstLine(skill),
		manifest: man,
		dir:      dir,
		dataDir:  m.dataDir(name),
	}, true
}

func (m *Manager) lookup(name string) (appDef, bool) {
	if b, ok := m.builtins[name]; ok {
		return b, true
	}
	return m.loadDiskApp(name)
}

func (m *Manager) allApps() []appDef {
	byName := map[string]appDef{}
	entries, err := os.ReadDir(filepath.Join(m.dir, "apps"))
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				if def, ok := m.loadDiskApp(e.Name()); ok {
					byName[def.name] = def
				}
			}
		}
	}
	for k, v := range m.builtins {
		byName[k] = v
	}
	names := slices.Sorted(maps.Keys(byName))
	out := make([]appDef, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out
}

// HasEntitlement reports whether app declares entitlement want in app.json.
func (m *Manager) HasEntitlement(app, want string) bool {
	def, ok := m.lookup(app)
	return ok && slices.Contains(def.manifest.Entitlements, want)
}

// Summary formats the apps system record body for an agent's session.
func (m *Manager) Summary(names []string, changed bool) string {
	lead := "Apps:\n"
	if changed {
		lead = "Apps (changed):\n"
	}
	if len(names) == 0 {
		if changed {
			return "Apps (changed): none\n"
		}
		return "Apps: none\n"
	}
	var b strings.Builder
	b.WriteString(lead)
	for _, n := range names {
		if def, ok := m.lookup(n); ok && def.desc != "" {
			fmt.Fprintf(&b, "- %s: %s\n", n, def.desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	b.WriteString("Read full app instructions on demand with `mainplane skill <app>` (GET $MAINPLANE_URL/skills/<app>) and call HTTP apps with `mainplane app <app> <METHOD> <path> [body]` ($MAINPLANE_URL/apps/<app>).\n")
	return b.String()
}

func (m *Manager) missingSecrets(man Manifest) ([]string, error) {
	out := []string{}
	for _, s := range man.Secrets {
		_, ok, err := m.Secrets.Get(s)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *Manager) daemonFor(def appDef) *daemon {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.daemons[def.name]
	if !ok {
		d = newDaemon(def.name, def.dir, def.dataDir)
		m.daemons[def.name] = d
	}
	return d
}

func (m *Manager) daemonEnv(def appDef) ([]string, error) {
	var env []string
	if m.loopback != nil {
		if u := m.loopback(); u != "" {
			env = append(env, "MAINPLANE_URL="+u)
		}
	}
	if len(def.manifest.Entitlements) > 0 && m.appToken != nil {
		if tok := m.appToken(def.name); tok != "" {
			env = append(env, "MAINPLANE_APP_KEY="+tok)
		}
	}
	for _, s := range def.manifest.Secrets {
		v, ok, err := m.Secrets.Get(s)
		if err != nil {
			return nil, err
		}
		if ok {
			env = append(env, s+"="+v)
		}
	}
	return env, nil
}

func (m *Manager) reconcileDaemons() {
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	for _, def := range m.allApps() {
		if def.kind != KindDaemon {
			continue
		}
		d := m.daemonFor(def)
		missing, err := m.missingSecrets(def.manifest)
		if err != nil {
			log.Printf("app %s: secrets: %v", def.name, err)
			continue
		}
		if len(missing) > 0 {
			d.stop()
			continue
		}
		d.mu.Lock()
		enabled := d.enabled
		d.mu.Unlock()
		if enabled {
			env, err := m.daemonEnv(def)
			if err != nil {
				log.Printf("app %s: secrets: %v", def.name, err)
				continue
			}
			if err := d.ensure(ctx, ctx, def.manifest, env); err != nil {
				log.Printf("app %s: start: %v", def.name, err)
			}
		}
	}
}

// StartApp enables and starts a daemon app.
func (m *Manager) StartApp(name string) error {
	def, ok := m.lookup(name)
	if !ok {
		return fmt.Errorf("no app %q", name)
	}
	if def.kind != KindDaemon {
		return fmt.Errorf("app %q is kind %s, not a daemon", name, def.kind)
	}
	d := m.daemonFor(def)
	d.enable()
	missing, err := m.missingSecrets(def.manifest)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return nil
	}
	env, err := m.daemonEnv(def)
	if err != nil {
		return err
	}
	m.mu.Lock()
	ctx := m.ctx
	m.mu.Unlock()
	return d.ensure(ctx, ctx, def.manifest, env)
}

// StopApp disables and stops a daemon app.
func (m *Manager) StopApp(name string) error {
	def, ok := m.lookup(name)
	if !ok {
		return fmt.Errorf("no app %q", name)
	}
	if def.kind != KindDaemon {
		return fmt.Errorf("app %q is kind %s, not a daemon", name, def.kind)
	}
	m.daemonFor(def).disable()
	return nil
}

// List returns the status of every installed app.
func (m *Manager) List() ([]AppEntry, error) {
	defs := m.allApps()
	out := make([]AppEntry, 0, len(defs))
	for _, def := range defs {
		missing, err := m.missingSecrets(def.manifest)
		if err != nil {
			return nil, err
		}
		status := "running"
		if len(missing) > 0 {
			status = "missing_secrets"
		}
		if def.kind == KindDaemon {
			status = m.daemonFor(def).status(missing)
		}
		ents := def.manifest.Entitlements
		if ents == nil {
			ents = []string{}
		}
		out = append(out, AppEntry{
			Name:           def.name,
			Description:    def.desc,
			Kind:           def.kind,
			Status:         status,
			Entitlements:   ents,
			MissingSecrets: missing,
		})
	}
	return out, nil
}

// Register mounts /secrets, /skills, and /apps onto mux.
func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /secrets", m.serveSecretsList)
	mux.HandleFunc("PUT /secrets/{name}", m.serveSecretPut)
	mux.HandleFunc("DELETE /secrets/{name}", m.serveSecretDelete)
	mux.HandleFunc("GET /skills", m.serveSkillsList)
	mux.HandleFunc("GET /skills/{app}", m.serveSkillGet)
	mux.HandleFunc("GET /apps", m.serveAppsList)
	mux.HandleFunc("/apps/{app}", m.serveApp)
	mux.HandleFunc("/apps/{app}/{path...}", m.serveApp)
}

// RegisterWebhooks mounts unauthenticated POST /webhooks/{app} onto mux.
func (m *Manager) RegisterWebhooks(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/{app}", m.serveWebhook)
	mux.HandleFunc("POST /webhooks/{app}/{path...}", m.serveWebhook)
}

func (m *Manager) serveSecretsList(w http.ResponseWriter, _ *http.Request) {
	names, err := m.Secrets.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, names)
}

func (m *Manager) serveSecretPut(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxSecret+1))
	if err != nil || len(b) > maxSecret {
		http.Error(w, "secret value too large", http.StatusBadRequest)
		return
	}
	if err := m.Secrets.Set(r.PathValue("name"), string(b)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) serveSecretDelete(w http.ResponseWriter, r *http.Request) {
	if err := m.Secrets.Delete(r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) serveSkillsList(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	var allow []string
	if caller.Kind == auth.Session {
		ag, ok := m.host.LiveAgent(caller.Session)
		if !ok {
			http.Error(w, "session has no live agent", http.StatusForbidden)
			return
		}
		allow = ag.Apps
	}
	out := []SkillEntry{}
	for _, def := range m.allApps() {
		if def.skill == "" {
			continue
		}
		if caller.Kind == auth.Session && !slices.Contains(allow, def.name) {
			continue
		}
		out = append(out, SkillEntry{Name: def.name, Description: def.desc})
	}
	writeJSON(w, out)
}

func (m *Manager) serveSkillGet(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	caller := auth.CallerFrom(r.Context())
	if caller.Kind == auth.Session {
		ag, ok := m.host.LiveAgent(caller.Session)
		if !ok || !slices.Contains(ag.Apps, app) {
			http.Error(w, "app not permitted for this session", http.StatusForbidden)
			return
		}
	}
	def, ok := m.lookup(app)
	if !ok || def.skill == "" {
		http.Error(w, "skill not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(def.skill))
}

func (m *Manager) serveAppsList(w http.ResponseWriter, _ *http.Request) {
	list, err := m.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

func (m *Manager) serveApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("app")
	sub := r.PathValue("path")
	targetPath := "/"
	if sub != "" {
		targetPath = "/" + sub
	}
	caller := auth.CallerFrom(r.Context())

	// Admin lifecycle routes: POST /apps/{name}/start and POST /apps/{name}/stop.
	if caller.Kind == auth.Key && r.Method == http.MethodPost && (sub == "start" || sub == "stop") {
		if def, ok := m.lookup(name); !ok || def.kind == KindDaemon {
			var err error
			if sub == "start" {
				err = m.StartApp(name)
			} else {
				err = m.StopApp(name)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	hdr := cleanHeaders(r.Header)
	if caller.Kind == auth.Session {
		ag, ok := m.host.LiveAgent(caller.Session)
		if !ok || !slices.Contains(ag.Apps, name) {
			http.Error(w, "app not permitted for this session", http.StatusForbidden)
			return
		}
		hdr.Set("X-Mainplane-Agent", ag.ID)
		hdr.Set("X-Mainplane-Session", caller.Session)
	}
	def, ok := m.lookup(name)
	if !ok {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	m.dispatch(w, r, def, targetPath, hdr)
}

func (m *Manager) serveWebhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("app")
	sub := r.PathValue("path")
	def, ok := m.lookup(name)
	if !ok || !def.manifest.Webhooks {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}
	targetPath := "/webhooks"
	if sub != "" {
		targetPath = "/webhooks/" + sub
	}
	hdr := cleanHeaders(r.Header)
	m.dispatch(w, r, def, targetPath, hdr)
}

func (m *Manager) dispatch(w http.ResponseWriter, r *http.Request, def appDef, targetPath string, hdr http.Header) {
	missing, err := m.missingSecrets(def.manifest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(missing) > 0 {
		http.Error(w, fmt.Sprintf("app %s: missing secrets: %s", def.name, strings.Join(missing, ", ")), http.StatusServiceUnavailable)
		return
	}
	switch def.kind {
	case KindBuiltin:
		req := r.Clone(r.Context())
		req.Header = hdr
		req.URL.Path = targetPath
		req.URL.RawPath = ""
		def.handler.ServeHTTP(w, req)
	case KindProxy:
		m.proxyURL(w, r, def, targetPath, hdr)
	case KindDaemon:
		env, err := m.daemonEnv(def)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d := m.daemonFor(def)
		m.mu.Lock()
		ctx := m.ctx
		m.mu.Unlock()
		if err := d.ensure(ctx, r.Context(), def.manifest, env); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		d.proxy(w, r, targetPath, hdr)
	default:
		http.Error(w, fmt.Sprintf("app %s is a text-only skill; read GET /skills/%s", def.name, def.name), http.StatusNotFound)
	}
}

func (m *Manager) proxyURL(w http.ResponseWriter, r *http.Request, def appDef, targetPath string, hdr http.Header) {
	base, err := url.Parse(def.manifest.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allowed := map[string]string{}
	for _, s := range def.manifest.Secrets {
		v, ok, err := m.Secrets.Get(s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			allowed[s] = v
		}
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = base.Scheme
			pr.Out.URL.Host = base.Host
			pr.Out.URL.Path = joinURLPath(base.Path, targetPath)
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = base.RawQuery
			if r.URL.RawQuery != "" {
				if pr.Out.URL.RawQuery != "" {
					pr.Out.URL.RawQuery += "&"
				}
				pr.Out.URL.RawQuery += r.URL.RawQuery
			}
			pr.Out.Host = base.Host
			pr.Out.Header = hdr.Clone()
			for k, v := range def.manifest.Headers {
				pr.Out.Header.Set(k, os.Expand(v, func(key string) string { return allowed[key] }))
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("app %s: %v", def.name, err), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func joinURLPath(a, b string) string {
	a = strings.TrimSuffix(a, "/")
	if b == "" || b == "/" {
		if a == "" {
			return "/"
		}
		return a
	}
	return a + "/" + strings.TrimPrefix(b, "/")
}

func cleanHeaders(in http.Header) http.Header {
	out := http.Header{}
	for k, vs := range in {
		lk := strings.ToLower(k)
		if lk == "authorization" || strings.HasPrefix(lk, "x-mainplane-") {
			continue
		}
		out[k] = slices.Clone(vs)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
