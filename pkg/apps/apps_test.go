package apps

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/apps/schedule"
	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// TestMain is also the daemon app of the tests: the manager runs this
// binary with a proxy secret, and it serves what it was given, or with a
// mode is a daemon of the tree tests.
func TestMain(m *testing.M) {
	if os.Getenv("MAINPLANE_PROXY_SECRET") != "" {
		if len(os.Args) > 1 {
			treeDaemon(os.Args[1])
			return
		}
		serveDaemon()
		return
	}
	os.Exit(m.Run())
}

// serveDaemon answers /env with its environment and the caller the harness
// named, and /post/<agent> and /list by calling the harness with its key.
func serveDaemon() {
	call := func(w http.ResponseWriter, method, path string) {
		req, _ := http.NewRequest(method, os.Getenv("MAINPLANE_URL")+path, strings.NewReader("from the app"))
		req.Header.Set("Authorization", "Bearer "+os.Getenv("MAINPLANE_APP_KEY"))
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
	_ = http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get(proxyHeader) != os.Getenv("MAINPLANE_PROXY_SECRET"):
			http.Error(w, "not the harness", http.StatusForbidden)
		case r.URL.Path == "/env":
			_ = json.NewEncoder(w).Encode(map[string]string{"agent": r.Header.Get(agentHeader), "key": os.Getenv("MAINPLANE_APP_KEY"), "K": os.Getenv("K"), "data": os.Getenv("DATA_DIR"), "environ": strings.Join(os.Environ(), "\n")})
		case strings.HasPrefix(r.URL.Path, "/post/"):
			call(w, http.MethodPost, "/agents/"+strings.TrimPrefix(r.URL.Path, "/post/")+"/records?via=spoofed")
		case r.URL.Path == "/list":
			call(w, http.MethodGet, "/agents")
		case r.URL.Path == "/sessions":
			call(w, http.MethodGet, "/sessions")
		}
	}))
}

type rig struct {
	t   *testing.T
	h   *harness.Harness
	m   *Manager
	k   ed25519.PrivateKey
	dir string
	url string
}

// newRig is a harness with a model that answers "ok" to every step, its
// apps, and the front door on a loopback port. The full key is "full".
func newRig(t *testing.T) *rig {
	t.Setenv("PROVIDER_KEY", "never an app's")
	t.Setenv("LANG", "C.UTF-8") // a daemon's on Unix, set here or not
	// The folder is made before the cancel is set, so at the end the daemons
	// are told to end before their folders go: Windows removes none in use.
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, k, _ := ed25519.GenerateKey(nil)
	p := provider.Anthropic("", "k")
	p.Run = func(ctx context.Context, body []byte, cache *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("ok")})
		return statefile.Header{Stop: "end_turn", Usage: &statefile.Usage{Input: 10}}, nil
	}
	h := &harness.Harness{
		Sessions:  statefile.Sessions{Dir: filepath.Join(dir, "sessions")},
		Providers: map[string]provider.Provider{"fake": p},
		Workers:   harness.NewPool(func(string) worker.Desired { return worker.Desired{} }, func(string) string { return "" }),
		Prompt:    func() ([]string, error) { return nil, nil },
		Agents:    filepath.Join(dir, "agents"),
	}
	if err := os.MkdirAll(h.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.LoadAgents(); err != nil {
		t.Fatal(err)
	}
	var front http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { front.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	m, err := New(ctx, h, dir, k, "hk", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	routes := http.NewServeMux()
	routes.Handle("/", harness.Handler(ctx, h))
	m.Routes(routes)
	front = m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer full" {
			http.Error(w, "api key refused", http.StatusUnauthorized)
			return
		}
		routes.ServeHTTP(w, r)
	}), routes)
	return &rig{t, h, m, k, dir, srv.URL}
}

func conf(workers ...string) *harness.AgentConfig {
	c := &harness.AgentConfig{Conf: statefile.Conf{Model: "fake/m1", ContextLimit: 1000, Workers: []statefile.Worker{}}}
	for _, w := range workers {
		c.Workers = append(c.Workers, statefile.Worker{Name: w})
	}
	return c
}

func (r *rig) agent(c *harness.AgentConfig, apps ...string) harness.Agent {
	r.t.Helper()
	a, err := r.h.CreateAgent(context.Background(), harness.AgentCreate{Config: c, Apps: apps}, "test")
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

// key is the session key a worker of the agent's live session holds.
func (r *rig) key(a harness.Agent) string {
	return auth.Sign(r.k, auth.Session, "hk", a.Sessions[len(a.Sessions)-1])
}

// do is one request with a bearer key: the status and the body.
func (r *rig) do(key, method, path, body string, header ...string) (int, string) {
	r.t.Helper()
	req, _ := http.NewRequest(method, r.url+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// want fails unless the request answers code.
func (r *rig) want(code int, key, method, path, body string, header ...string) string {
	r.t.Helper()
	got, text := r.do(key, method, path, body, header...)
	if got != code {
		r.t.Fatalf("%s %s: %d %s, want %d", method, path, got, text, code)
	}
	return text
}

// vias is the via of each message in the agent's live session.
func (r *rig) vias(id string) []string {
	r.t.Helper()
	a, err := r.h.Agent(id)
	if err != nil {
		r.t.Fatal(err)
	}
	chain, err := r.h.Sessions.Load(a.Sessions[len(a.Sessions)-1])
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, rec := range chain {
		if rec.Kind == statefile.Message {
			out = append(out, rec.Via)
		}
	}
	return out
}

// settled waits for the agent's live session to end its run.
func (r *rig) settled(id string) {
	r.t.Helper()
	r.until(func() bool {
		i, err := r.h.AgentInfo(id)
		return err == nil && i.Status == statefile.StatusClosed
	})
}

func (r *rig) until(ok func() bool) {
	r.t.Helper()
	for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			r.t.Fatal("timed out waiting")
		}
	}
}

// install writes an app's folder.
func (r *rig) install(name string, spec Spec) {
	r.t.Helper()
	dir := filepath.Join(r.dir, "apps", name)
	b, _ := json.Marshal(spec)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "skill.md"), []byte("# "+name+": a test app.\nmore\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "app.json"), b, 0o644)
}

// skey-revoke: a session's key reaches its agent's apps while the session is
// live and the agent has the app, and nothing else ever.
func TestSessionKeyRevokes(t *testing.T) {
	r := newRig(t)
	a := r.agent(conf(), "message", "schedule")
	k1 := r.key(a)
	if got := r.want(200, k1, "GET", "/skills", ""); !strings.Contains(got, `"schedule"`) || strings.Contains(got, "orchestrate") {
		t.Fatalf("skills: %s", got)
	}
	if got := r.want(200, k1, "GET", "/skills/schedule", ""); got != schedule.Skill {
		t.Fatalf("skill: %s", got)
	}
	r.want(200, k1, "GET", "/apps/schedule", "")
	r.want(403, k1, "GET", "/skills/orchestrate", "")
	r.want(403, k1, "GET", "/apps/orchestrate", "")
	r.want(403, k1, "GET", "/sessions", "")
	r.want(403, k1, "GET", "/agents/"+a.ID, "")
	r.want(403, k1, "PUT", "/secrets/K", "v")
	r.want(401, k1+"x", "GET", "/skills", "")
	r.want(401, "", "GET", "/skills", "")
	plain, err := r.h.Create(context.Background(), harness.Create{Model: "fake/m1", ContextLimit: 1000}, "tui")
	if err != nil {
		t.Fatal(err)
	}
	r.want(403, auth.Sign(r.k, auth.Session, "hk", plain), "GET", "/skills", "")

	// the app is taken away
	r.want(204, "full", "PUT", "/agents/"+a.ID+"/apps?via=test", `["message"]`)
	r.want(403, k1, "GET", "/apps/schedule", "")
	r.want(403, k1, "GET", "/skills/schedule", "")
	r.want(200, k1, "GET", "/skills/message", "")

	// the agent moves on
	c := conf()
	c.Model = "fake/m2"
	b, _ := json.Marshal(c)
	r.want(204, "full", "PUT", "/agents/"+a.ID+"/config?via=test", string(b))
	r.want(200, k1, "GET", "/skills/message", "") // an edit alone moves nothing
	r.want(200, "full", "POST", "/agents/"+a.ID+"/records?via=test", "go on", "Content-Type", "text/plain")
	r.want(403, k1, "GET", "/skills/message", "")
	r.want(403, k1, "POST", "/apps/message", `{"to":"`+a.ID+`","text":"x"}`)
	a, _ = r.h.Agent(a.ID)
	if len(a.Sessions) != 2 {
		t.Fatalf("sessions: %v", a.Sessions)
	}
	r.want(200, r.key(a), "GET", "/skills/message", "")
}

// secret-iso, the proxy, and webhooks: an app gets the secrets it names and
// the harness's word on who calls, and an outside caller gets one door.
func TestProxySecretsWebhooks(t *testing.T) {
	r := newRig(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"path": req.URL.Path, "query": req.URL.RawQuery, "h": req.Header})
	}))
	defer up.Close()
	r.install("prox", Spec{URL: up.URL + "/base", Secrets: []string{"K"}, Headers: map[string]string{"X-Api-Key": "$K"}})
	r.install("bare", Spec{URL: up.URL, Headers: map[string]string{"X-Api-Key": "$K"}, Webhooks: true})
	r.install("text", Spec{})
	a := r.agent(conf(), "prox", "bare", "text")
	key := r.key(a)
	seen := func(body string) (path string, h http.Header) {
		var v struct {
			Path string
			H    http.Header
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		return v.Path, v.H
	}

	if got := r.want(503, key, "GET", "/apps/prox/x", ""); !strings.Contains(got, "K") {
		t.Fatalf("missing secret not named: %s", got)
	}
	if got := r.want(200, "full", "GET", "/apps", ""); !strings.Contains(got, `"status":"missing_secrets"`) || !strings.Contains(got, `"kind":"built-in"`) {
		t.Fatalf("apps: %s", got)
	}
	const value = `s3cret=with "quotes"`
	for _, blank := range []string{"", " \n"} {
		r.want(400, "full", "PUT", "/secrets/K", blank)
	}
	if got := r.want(200, "full", "GET", "/secrets", ""); strings.TrimSpace(got) != `[]` {
		t.Fatalf("a secret with no value was kept: %s", got)
	}
	r.want(204, "full", "PUT", "/secrets/K", value)
	if got := r.want(200, "full", "GET", "/secrets", ""); strings.TrimSpace(got) != `["K"]` {
		t.Fatalf("secrets: %s", got)
	}
	if fi, err := os.Stat(filepath.Join(r.dir, ".secrets")); err != nil || fi.Mode().Perm() != 0o600 && runtime.GOOS != "windows" { // Windows has no such mode: the folder's ACL is what guards the file
		t.Fatalf(".secrets: %v %v", fi, err)
	}
	path, h := seen(r.want(200, key, "POST", "/apps/prox/echo?q=1", "{}", "X-Mainplane-Agent", "forged", "X-Mainplane-Proxy-Secret", "forged"))
	if path != "/base/echo" || h.Get("X-Api-Key") != value || h.Get(agentHeader) != a.ID || h.Get(sessHeader) != a.Sessions[0] || h.Get("Authorization") != "" || h.Get(proxyHeader) != "" {
		t.Fatalf("proxied: %s %v", path, h)
	}
	if _, h := seen(r.want(200, key, "GET", "/apps/bare", "")); h.Get("X-Api-Key") != "" {
		t.Fatalf("an app that names no secret got one: %v", h)
	}
	r.want(404, key, "GET", "/apps/text/x", "")
	if got := r.want(200, key, "GET", "/skills", ""); !strings.Contains(got, `{"name":"text","description":"a test app."}`) {
		t.Fatalf("skills: %s", got)
	}

	path, h = seen(r.want(200, "", "POST", "/webhooks/bare/events", "{}", "X-Mainplane-Agent", "forged", "Authorization", "Basic sender"))
	if path != "/webhooks/events" || h.Get(agentHeader) != "" || h.Get("Authorization") != "Basic sender" { // the sender's word to the app goes through
		t.Fatalf("webhook: %s %v", path, h)
	}
	r.want(404, "", "POST", "/webhooks/prox/events", "{}")
	r.want(404, "", "GET", "/webhooks/bare/events", "")

	r.want(204, "full", "DELETE", "/secrets/K", "")
	r.want(503, key, "GET", "/apps/prox/x", "")
}

// run daemons and akey-scope: a daemon gets its port, its secrets and no
// more; its own key only with entitlements, and that key reaches only the
// agents that have the app, under the app's name.
func TestDaemonAndAppKey(t *testing.T) {
	r := newRig(t)
	r.install("d0", Spec{Run: []string{os.Args[0]}})
	r.install("d1", Spec{Run: []string{os.Args[0]}, Secrets: []string{"K"}, Entitlements: []string{"post", "list"}})
	a, b := r.agent(conf(), "d0", "d1"), r.agent(conf(), "d0")
	key := r.key(a)
	env := func(app string) map[string]string {
		var v map[string]string
		if err := json.Unmarshal([]byte(r.want(200, key, "GET", "/apps/"+app+"/env", "")), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	r.want(503, key, "GET", "/apps/d0/env", "") // installed after the harness started: not yet run
	r.want(204, "full", "POST", "/apps/d0/start", "")
	e0 := env("d0")
	if e0["key"] != "" || e0["K"] != "" || e0["agent"] != a.ID || e0["data"] != filepath.Join(r.dir, "app-data", "d0") || strings.Contains(e0["environ"], "PROVIDER_KEY") {
		t.Fatalf("d0: %v", e0)
	}
	// of the harness's environment a daemon has the names in inherit that are set, and no other
	for _, n := range inherit {
		if v, ok := os.LookupEnv(n); ok && !strings.Contains("\n"+e0["environ"]+"\n", "\n"+n+"="+v+"\n") {
			t.Fatalf("d0 has no %s=%s: %s", n, v, e0["environ"])
		}
	}
	for line := range strings.SplitSeq(e0["environ"], "\n") {
		n, _, _ := strings.Cut(line, "=")
		if !slices.Contains(inherit, strings.ToUpper(n)) && !slices.Contains([]string{"PORT", "DATA_DIR", "MAINPLANE_URL", "MAINPLANE_PROXY_SECRET"}, n) {
			t.Fatalf("d0 has %s", line)
		}
	}
	r.want(401, key, "GET", "/apps/d0/list", "") // no entitlement, so no key at all

	r.want(503, key, "GET", "/apps/d1/env", "")
	r.want(204, "full", "PUT", "/secrets/K", "v1") // starts the app that waited for it
	e := env("d1")
	if name, ok := auth.Signed(r.k, auth.App, e["key"]); !ok || name != "d1" || e["K"] != "v1" {
		t.Fatalf("d1: %v", e)
	}
	r.want(200, key, "POST", "/apps/d1/post/"+a.ID, "")
	if got := r.vias(a.ID); !slices.Equal(got, []string{"app:d1"}) {
		t.Fatalf("vias: %v", got)
	}
	r.want(403, key, "POST", "/apps/d1/post/"+b.ID, "")
	r.want(404, key, "POST", "/apps/d1/post/00000000", "")
	if got := r.want(200, key, "GET", "/apps/d1/list", ""); !strings.Contains(got, a.ID) || strings.Contains(got, b.ID) {
		t.Fatalf("list: %s", got)
	}
	r.want(403, key, "GET", "/apps/d1/sessions", "")
	r.want(204, "full", "PUT", "/agents/"+a.ID+"/apps?via=test", `["d0"]`)
	r.want(403, e["key"], "POST", "/agents/"+a.ID+"/records", "x", "Content-Type", "text/plain")

	// the port takes nothing that is not the harness
	r.m.mu.Lock()
	port := r.m.daemons["d1"].port
	r.m.mu.Unlock()
	resp, err := http.Get(fmt.Sprintf("http://%s/env", net.JoinHostPort("127.0.0.1", fmt.Sprint(port))))
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("direct: %v %v", resp, err)
	}
	r.want(204, "full", "POST", "/apps/d0/stop", "")
	if got := r.want(200, "full", "GET", "/apps", ""); !strings.Contains(got, `"name":"d0","description":"a test app.","kind":"daemon","status":"stopped"`) || !strings.Contains(got, `"kind":"daemon","status":"running","entitlements":["post","list"]`) {
		t.Fatalf("apps: %s", got)
	}
	r.want(503, r.key(b), "GET", "/apps/d0/env", "")
	r.want(400, "full", "POST", "/apps/message/start", "")
}

// orchestrate and message: a spawned agent is the caller's, within what the
// caller has; nothing wakes the caller unless it asked.
func TestOrchestrateAndMessage(t *testing.T) {
	r := newRig(t)
	a := r.agent(conf("w1"), "orchestrate", "message")
	other := r.agent(conf("w1"), "orchestrate")
	key := r.key(a)
	spawn := func(body string) string {
		var v struct{ ID, Session string }
		if err := json.Unmarshal([]byte(r.want(200, key, "POST", "/apps/orchestrate/spawn", body)), &v); err != nil || v.ID == "" || v.Session == "" {
			t.Fatalf("spawn: %v %v", v, err)
		}
		return v.ID
	}
	c := spawn(`{"name":"child","apps":["message"],"message":"the task"}`)
	child, err := r.h.Agent(c)
	if err != nil || child.Via != "agent:"+a.ID || !slices.Equal(child.Apps, []string{"message"}) || child.Config.Workers[0].Name != "w1" {
		t.Fatalf("child: %+v %v", child, err)
	}
	r.settled(c)
	if got := r.vias(c); !slices.Equal(got, []string{"agent:" + a.ID}) {
		t.Fatalf("child vias: %v", got)
	}
	two, _ := json.Marshal(map[string]any{"config": conf("w1", "w2")})
	r.want(403, key, "POST", "/apps/orchestrate/spawn", string(two))
	r.want(403, key, "POST", "/apps/orchestrate/spawn", `{"apps":["schedule"]}`)
	r.want(403, key, "PUT", "/apps/orchestrate/"+c, `{"apps":["schedule"]}`)
	r.want(403, key, "PUT", "/apps/orchestrate/"+c, string(two))
	r.want(403, r.key(child), "POST", "/apps/orchestrate/spawn", `{}`) // a leaf has no orchestrate

	// the child's own message app reaches the parent, who needs none to receive
	r.want(204, r.key(child), "POST", "/apps/message", `{"to":"`+other.ID+`","text":"done"}`)
	r.want(404, r.key(child), "POST", "/apps/message", `{"to":"00000000","text":"done"}`)
	r.settled(other.ID)
	if got := r.vias(other.ID); !slices.Equal(got, []string{"agent:" + c}) {
		t.Fatalf("other vias: %v", got)
	}

	// a key is its sender's: the same one from two agents is two messages, and a repeat of one is none
	for _, k := range []string{key, r.key(child), r.key(child)} {
		r.want(204, k, "POST", "/apps/message", `{"to":"`+other.ID+`","text":"keyed"}`, "Idempotency-Key", "k1")
		r.settled(other.ID)
	}
	if got := r.vias(other.ID); !slices.Equal(got, []string{"agent:" + c, "agent:" + a.ID, "agent:" + c}) {
		t.Fatalf("other vias after keyed posts: %v", got)
	}

	n := spawn(`{"notify_on_stop":true,"message":"task two"}`)
	r.until(func() bool { return slices.Contains(r.vias(a.ID), "app:orchestrate") })
	r.settled(a.ID)
	r.want(204, key, "POST", "/apps/message", `{"to":"`+n+`","text":"more"}`)
	r.until(func() bool { return len(r.vias(n)) == 2 })
	r.settled(n)
	r.settled(a.ID)
	if got := r.vias(a.ID); !slices.Equal(got, []string{"app:orchestrate"}) { // once, and nothing for the silent child
		t.Fatalf("parent vias: %v", got)
	}

	if got := r.want(200, key, "GET", "/apps/orchestrate", ""); !strings.Contains(got, c) || !strings.Contains(got, n) || strings.Contains(got, other.ID) {
		t.Fatalf("list: %s", got)
	}
	if got := r.want(200, key, "GET", "/apps/orchestrate/"+c, ""); !strings.Contains(got, `"status":"closed"`) {
		t.Fatalf("get: %s", got)
	}
	if got := r.want(200, key, "GET", "/apps/orchestrate/"+c+"/records", ""); !strings.Contains(got, "the task") {
		t.Fatalf("records: %s", got)
	}
	r.want(404, r.key(other), "GET", "/apps/orchestrate/"+c, "") // not its child
	r.want(204, key, "PUT", "/apps/orchestrate/"+c, `{"name":"renamed","apps":[]}`)
	if child, _ = r.h.Agent(c); child.Name != "renamed" || len(child.Apps) != 0 {
		t.Fatalf("update: %+v", child)
	}
	r.want(204, key, "POST", "/apps/orchestrate/"+c+"/stop", "")
	r.want(204, key, "DELETE", "/apps/orchestrate/"+c, "")
	r.want(404, key, "GET", "/apps/orchestrate/"+c, "")
}

// A spawn that fails after its agent was made leaves no agent, no session
// and no watch: the caller was told no, and may try again.
func TestSpawnUndone(t *testing.T) {
	r := newRig(t)
	a := r.agent(conf("w1"), "orchestrate")
	key := r.key(a)
	all := func(harness.Agent) bool { return true }
	block := filepath.Join(r.dir, "app-data", "orchestrate", "watches.json.tmp") // the watch cannot be written
	if err := os.Mkdir(block, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := func() int {
		names, err := os.ReadDir(r.h.Sessions.Dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(names)
	}
	before := sessions()
	if code, got := r.do(key, "POST", "/apps/orchestrate/spawn", `{"notify_on_stop":true,"message":"the task"}`); code == 200 {
		t.Fatalf("a spawn whose watch was not kept: %s", got)
	}
	if got := r.want(200, key, "GET", "/apps/orchestrate", ""); strings.TrimSpace(got) != `[]` {
		t.Fatalf("children after a failed spawn: %s", got)
	}
	if n := len(r.h.ListAgents(all)); n != 1 || sessions() != before {
		t.Fatalf("after a failed spawn: %d agents, %d sessions, want 1 and %d", n, sessions(), before)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	r.want(200, key, "POST", "/apps/orchestrate/spawn", `{"notify_on_stop":true,"message":"the task"}`)
	if n := len(r.h.ListAgents(all)); n != 2 {
		t.Fatalf("after the next try: %d agents", n)
	}
	r.until(func() bool { return slices.Contains(r.vias(a.ID), "app:orchestrate") })
	r.settled(a.ID)
}

// schedule: a timer fires as a message from the app, and the timers are
// there after a restart.
func TestSchedule(t *testing.T) {
	r := newRig(t)
	a, b := r.agent(conf(), "schedule"), r.agent(conf(), "schedule")
	key := r.key(a)
	r.want(200, key, "POST", "/apps/schedule", `{"in":"30ms","text":"wake"}`)
	r.until(func() bool { return slices.Contains(r.vias(a.ID), "app:schedule") })
	r.want(200, key, "POST", "/apps/schedule", `{"in":"1h","text":"later"}`)
	var cron schedule.Timer
	if err := json.Unmarshal([]byte(r.want(200, key, "POST", "/apps/schedule", `{"cron":"*/5 * * * *","text":"often"}`)), &cron); err != nil || cron.Next.Minute()%5 != 0 {
		t.Fatalf("cron: %+v %v", cron, err)
	}
	r.want(400, key, "POST", "/apps/schedule", `{"in":"1h","cron":"* * * * *","text":"both"}`)
	r.want(400, key, "POST", "/apps/schedule", `{"cron":"61 * * * *","text":"bad"}`)
	r.want(200, r.key(b), "POST", "/apps/schedule", `{"at":"2999-01-01T00:00:00Z","text":"b's"}`)
	r.want(404, r.key(b), "DELETE", "/apps/schedule/"+cron.ID, "")

	// a harness that starts again reads the same timers, and fires what came due
	due, _ := json.Marshal([]schedule.Timer{{ID: "due", Agent: b.ID, Text: "missed", Next: time.Now().Add(-time.Hour)}, {ID: "gone", Agent: "00000000", Text: "x", Next: time.Now().Add(-time.Hour)}})
	dir := filepath.Join(r.dir, "restart")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "timers.json"), due, 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := schedule.New(ctx, r.h, dir); err != nil {
		t.Fatal(err)
	}
	r.until(func() bool { return slices.Contains(r.vias(b.ID), "app:schedule") })
	r.until(func() bool {
		left, _ := os.ReadFile(filepath.Join(dir, "timers.json"))
		return strings.TrimSpace(string(left)) == "[]"
	})
	again, err := schedule.New(ctx, r.h, filepath.Join(r.dir, "app-data", "schedule"))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(agentHeader, a.ID)
	again.ServeHTTP(w, req)
	if got := w.Body.String(); !strings.Contains(got, "later") || !strings.Contains(got, "often") || strings.Contains(got, "b's") || strings.Contains(got, "wake") {
		t.Fatalf("timers after restart: %s", got)
	}
	// a change the file did not take is not in memory either
	block := filepath.Join(r.dir, "app-data", "schedule", "timers.json.tmp")
	if err := os.Mkdir(block, 0o755); err != nil {
		t.Fatal(err)
	}
	r.want(500, key, "POST", "/apps/schedule", `{"in":"1h","text":"refused"}`)
	r.want(500, key, "DELETE", "/apps/schedule/"+cron.ID, "")
	if got := r.want(200, key, "GET", "/apps/schedule", ""); strings.Contains(got, "refused") || !strings.Contains(got, "often") {
		t.Fatalf("timers after two failed saves: %s", got)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	r.want(204, key, "DELETE", "/apps/schedule/"+cron.ID, "")
	if got := r.want(200, key, "GET", "/apps/schedule", ""); strings.Contains(got, "often") {
		t.Fatalf("deleted timer listed: %s", got)
	}
}

// A secret cannot take the name of a variable the harness sets for a daemon,
// and an app.json cannot name one or set a header the harness speaks in.
// An app.json that could only fail at its first call, or whose entitlement
// would do nothing, is refused when it is read.
func TestSpecRefused(t *testing.T) {
	r := newRig(t)
	for _, u := range []string{"example.com", "example.com:8080/v1", "ftp://example.com", "http://", "https:///path", "/local", "http://[::1", "file:///etc/passwd"} {
		r.install("p", Spec{URL: u})
		if got := r.want(404, "full", "GET", "/skills/p", ""); !strings.Contains(got, "url") {
			t.Fatalf("app.json with url %s: %s", u, got)
		}
	}
	for _, e := range []string{"Post", "admin", "", "post,list"} {
		r.install("p", Spec{URL: "https://example.com", Entitlements: []string{"post", e}})
		if got := r.want(404, "full", "GET", "/skills/p", ""); !strings.Contains(got, "entitlement") {
			t.Fatalf("app.json with entitlement %q: %s", e, got)
		}
	}
	for _, u := range []string{"https://example.com", "http://127.0.0.1:8080/v1?k=1", "HTTP://[::1]:9"} {
		r.install("p", Spec{URL: u, Entitlements: []string{"post", "list"}})
		r.want(200, "full", "GET", "/skills/p", "")
	}
}

func TestReservedNames(t *testing.T) {
	r := newRig(t)
	for _, n := range []string{"PORT", "DATA_DIR", "PATH", "HOME", "Path", "MAINPLANE_URL", "MAINPLANE_PROXY_SECRET", "mainplane_app_key"} {
		if got := r.want(400, "full", "PUT", "/secrets/"+n, "v"); !strings.Contains(got, n) {
			t.Fatalf("%s: %s", n, got)
		}
		r.install("d", Spec{Run: []string{os.Args[0]}, Secrets: []string{"K", n}})
		if got := r.want(404, "full", "GET", "/skills/d", ""); !strings.Contains(got, "secret "+n) {
			t.Fatalf("app.json with secret %s: %s", n, got)
		}
		r.want(400, "full", "POST", "/apps/d/start", "")
	}
	if got := r.want(200, "full", "GET", "/secrets", ""); strings.TrimSpace(got) != "[]" {
		t.Fatalf("secrets: %s", got)
	}
	r.want(204, "full", "PUT", "/secrets/PORTS", "v") // only the names themselves
	// one written before the rule can still be removed
	if err := os.WriteFile(filepath.Join(r.dir, ".secrets"), []byte(`{"PORT":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.want(204, "full", "DELETE", "/secrets/PORT", "")

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, req.Header.Get(agentHeader)+" "+req.URL.RawQuery)
	}))
	defer up.Close()
	for _, k := range []string{"X-Mainplane-Agent", "x-mainplane-session", "X-MAINPLANE-ANYTHING"} {
		r.install("forge", Spec{URL: up.URL, Headers: map[string]string{k: "another"}})
		if got := r.want(404, "full", "GET", "/skills/forge", ""); !strings.Contains(got, "header "+k) {
			t.Fatalf("app.json with header %s: %s", k, got)
		}
	}
	// a base URL's query stays when the call has one of its own
	r.install("forge", Spec{URL: up.URL + "?base=1", Headers: map[string]string{"X-Other": "fine"}})
	a := r.agent(conf(), "forge")
	if got := r.want(200, r.key(a), "GET", "/apps/forge/x?own=2", ""); got != a.ID+" base=1&own=2" {
		t.Fatalf("forwarded: %s", got)
	}
}
