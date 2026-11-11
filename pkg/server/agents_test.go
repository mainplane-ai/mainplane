package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// TestMain is also the run app the tests install: the test binary with the
// argument daemon serves what an app sees.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		daemon()
		return
	}
	os.Exit(m.Run())
}

// daemon answers /env with what it was given, /post and /list with what its
// app key gets, and /webhooks/* with the agent header it saw.
func daemon() {
	mux := http.NewServeMux()
	call := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, os.Getenv("MAINPLANE_URL")+path, strings.NewReader("from the app"))
		req.Header.Set("Authorization", "Bearer "+os.Getenv("MAINPLANE_APP_KEY"))
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	mux.HandleFunc("/env", func(w http.ResponseWriter, r *http.Request) {
		env := map[string]string{"agent": r.Header.Get(apps.AgentHeader)}
		for _, k := range []string{"MAINPLANE_APP_KEY", "K", "DATA_DIR", "PORT"} {
			if v, ok := os.LookupEnv(k); ok {
				env[k] = v
			}
		}
		_ = json.NewEncoder(w).Encode(env)
	})
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		code, body := call("POST", "/agents/"+r.URL.Query().Get("to")+"/records?via=forged")
		_, _ = fmt.Fprintf(w, "%d %s", code, body)
	})
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		code, body := call("GET", "/agents")
		_, _ = fmt.Fprintf(w, "%d %s", code, body)
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		code, _ := call("GET", "/sessions")
		_, _ = fmt.Fprintf(w, "%d", code)
	})
	mux.HandleFunc("/webhooks/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "agent=%q path=%s", r.Header.Get(apps.AgentHeader), r.URL.Path)
	})
	check := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(apps.ProxyHeader) != os.Getenv("MAINPLANE_PROXY_SECRET") {
			http.Error(w, "not the harness", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
	_ = http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), check)
}

// rig is a harness with the server's wiring and a fake provider: each step
// emits what step says and reports used tokens of prompt.
type rig struct {
	t      *testing.T
	h      *harness.Harness
	url    string
	key    string
	signer auth.Signer
	steps  atomic.Int64
	stop   func()

	mu   sync.Mutex
	step func(n int64, body []byte) ([]statefile.Record, int)
	ttl  int
}

func text(s string) statefile.Record {
	return statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte(s)}
}

func newRig(t *testing.T, dir string) *rig {
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{t: t, ttl: 3600}
	k, err := Key(dir)
	must(t, err)
	p := provider.Anthropic("http://127.0.0.1:1", "")
	p.Run = func(ctx context.Context, body []byte, cache *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
		n := r.steps.Add(1)
		r.mu.Lock()
		step, ttl := r.step, r.ttl
		r.mu.Unlock()
		recs, used := []statefile.Record{text("ok")}, 100
		if step != nil {
			recs, used = step(n, body)
		}
		for _, rec := range recs {
			emit(rec)
		}
		cache.TTL = ttl
		return statefile.Header{Usage: &statefile.Usage{Input: used}, Stop: "end_turn"}, nil
	}
	must(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	pool := harness.NewPool(func(string) worker.Desired { return worker.Desired{} })
	r.signer = auth.Signer{Seed: k.Seed(), Harness: pointer.Encode(k)}
	r.h = &harness.Harness{Sessions: statefile.Sessions{Dir: filepath.Join(dir, "sessions")}, Providers: map[string]provider.Provider{"fake": p}, Workers: pool, Prompt: func() ([]string, error) { return nil, nil }, AgentDir: filepath.Join(dir, "agents"), Signer: r.signer}
	srv := httptest.NewUnstartedServer(nil)
	r.url = "http://" + srv.Listener.Addr().String()
	r.h.Apps = apps.New(ctx, dir, r.url, func(app string) string { return r.signer.Sign(auth.AppKey, app) })
	must(t, loadAgents(r.h))
	must(t, r.h.Apps.Builtin("echo", []byte("Echo the call.\n"), func(context.Context, string) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintf(w, "agent=%s session=%s path=%s auth=%q", req.Header.Get(apps.AgentHeader), req.Header.Get(apps.SessionHeader), req.URL.Path, req.Header.Get("Authorization"))
		}), nil
	}))
	pool.Env = r.h.Env
	store := Auth(dir)
	r.key, err = store.Issue(auth.Key, "test")
	if errors.Is(err, auth.ErrTaken) { // a restart on the same dir: a fresh name
		r.key, err = store.Issue(auth.Key, statefile.NewID())
	}
	must(t, err)
	mux := http.NewServeMux()
	mux.Handle("/", store.Bearer(&auth.Limit{}, r.signer, r.h.Gate(ctx, harness.Handler(ctx, r.h))))
	mux.HandleFunc("POST /webhooks/{app}/{path...}", func(w http.ResponseWriter, req *http.Request) {
		r.h.Apps.Webhook(w, req, req.PathValue("app"), req.PathValue("path"))
	})
	srv.Config.Handler = mux
	srv.Start()
	r.h.Apps.Up()
	must(t, r.h.Resume(ctx))
	r.stop = func() {
		cancel()
		r.h.Apps.Close()
		srv.Close()
	}
	t.Cleanup(r.stop)
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// do makes one call with bearer and returns its status and body.
func (r *rig) do(bearer, method, path, body string) (int, string) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.url+path, strings.NewReader(body))
	must(r.t, err)
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	if method == "POST" && strings.HasSuffix(strings.Split(path, "?")[0], "/records") {
		req.Header.Set("Content-Type", "text/plain")
	}
	req.Header.Set(apps.AgentHeader, "forged")
	resp, err := http.DefaultClient.Do(req)
	must(r.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ok is do with the api key, failing the test on a status outside 2xx; the
// reply lands in out when out is given.
func (r *rig) ok(method, path, body string, out any) {
	r.t.Helper()
	code, b := r.do(r.key, method, path, body)
	if code/100 != 2 {
		r.t.Fatalf("%s %s: %d %s", method, path, code, b)
	}
	if out != nil {
		must(r.t, json.Unmarshal([]byte(b), out))
	}
}

func conf(model string, limit, cold int, workers ...string) string {
	ws := []statefile.Worker{}
	for _, w := range workers {
		ws = append(ws, statefile.Worker{Name: w})
	}
	b, _ := json.Marshal(harness.AgentConfig{Conf: statefile.Conf{Model: model, ContextLimit: limit, Workers: ws}, ColdLimit: cold})
	return string(b)
}

// agent makes an agent and returns its id and first session.
func (r *rig) agent(config string, appNames ...string) (string, string) {
	r.t.Helper()
	b, _ := json.Marshal(appNames)
	var out struct{ ID, Session string }
	r.ok("POST", "/agents?via=web", `{"config":`+config+`,"apps":`+string(b)+`}`, &out)
	return out.ID, out.Session
}

func (r *rig) info(id string) harness.AgentInfo {
	r.t.Helper()
	var info harness.AgentInfo
	r.ok("GET", "/agents/"+id, "", &info)
	return info
}

func (r *rig) chain(sid string) []statefile.Record {
	r.t.Helper()
	chain, err := r.h.Sessions.Load(sid)
	must(r.t, err)
	return chain
}

// idle waits for agent id's live session to be closed or failed.
func (r *rig) idle(id string) harness.AgentInfo {
	r.t.Helper()
	for range 500 {
		if info := r.info(id); info.Status == statefile.StatusClosed || info.Status == statefile.StatusFailed {
			return info
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("agent %s never went idle", id)
	return harness.AgentInfo{}
}

// eventually waits for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func messages(chain []statefile.Record, via string) []string {
	var out []string
	for _, r := range chain {
		if r.Kind == statefile.Message && r.Via == via {
			out = append(out, string(r.Body))
		}
	}
	return out
}

func lastSystem(chain []statefile.Record, source string) string {
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Kind == statefile.System && chain[i].Source == source {
			return string(chain[i].Body)
		}
	}
	return ""
}

func TestCreate1Trip(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	id, sid := r.agent(conf("fake/m", 1000, 0), "message")
	var a harness.Agent
	b, err := os.ReadFile(filepath.Join(dir, "agents", id+".json"))
	must(t, err)
	must(t, json.Unmarshal(b, &a))
	if !slices.Equal(a.Sessions, []string{sid}) || a.Via != "web" {
		t.Fatalf("agent file: %+v", a)
	}
	if info := r.info(id); info.Status != statefile.StatusClosed || info.CurrentSession != sid {
		t.Fatalf("info: %+v", info)
	}
	if n := r.steps.Load(); n != 0 {
		t.Fatalf("%d LLM calls", n)
	}
	if note := lastSystem(r.chain(sid), "apps"); !strings.Contains(note, "agent "+id) || !strings.Contains(note, "- message: ") {
		t.Fatalf("apps note: %q", note)
	}
	if code, _ := r.do(r.key, "POST", "/agents?via=web", `{"apps":["nope"],"config":`+conf("fake/m", 1000, 0)+`}`); code != 400 {
		t.Fatalf("unknown app: %d", code)
	}
}

func TestCreate2TripAndVia(t *testing.T) {
	r := newRig(t, t.TempDir())
	var s struct{ ID string }
	r.ok("POST", "/sessions?via=tui", `{"model":"fake/m","context_limit":1000}`, &s)
	var out struct{ ID, Session string }
	r.ok("POST", "/agents?via=web", `{"session":"`+s.ID+`","apps":["schedule"]}`, &out)
	if out.Session != s.ID {
		t.Fatalf("adopted %s, want %s", out.Session, s.ID)
	}
	chain := r.chain(s.ID)
	if chain[0].Via != "tui" || chain[len(chain)-1].Source != "apps" {
		t.Fatalf("record 1 via %q, tip %+v", chain[0].Via, chain[len(chain)-1].Header)
	}
	if info := r.info(out.ID); info.Config.Model != "fake/m" || info.Config.ContextLimit != 1000 {
		t.Fatalf("config not from the session: %+v", info.Config)
	}
	if code, b := r.do(r.key, "POST", "/agents?via=web", `{"session":"`+s.ID+`"}`); code != 400 || !strings.Contains(b, "belongs to agent "+out.ID) {
		t.Fatalf("second claim: %d %s", code, b)
	}
	// via-start: an agent's own session names it; Build never sends Start.
	id, sid := r.agent(conf("fake/m", 1000, 0))
	if v := r.chain(sid)[0].Via; v != "agent:"+id {
		t.Fatalf("agent session via %q", v)
	}
	built, _ := statefile.Build(r.chain(sid))
	if slices.ContainsFunc(built, func(rec statefile.Record) bool { return rec.Kind == statefile.Start }) {
		t.Fatal("Build sent a start record")
	}
}

func TestLazyConfig(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	id, s1 := r.agent(conf("fake/a", 1000, 0))
	before, _ := os.ReadDir(filepath.Join(dir, "sessions"))
	r.ok("PUT", "/agents/"+id+"/config?via=web", conf("fake/b", 1000, 0), nil)
	r.ok("PUT", "/agents/"+id+"/config?via=web", conf("fake/c", 1000, 0), nil)
	after, _ := os.ReadDir(filepath.Join(dir, "sessions"))
	if len(after) != len(before) || len(r.info(id).Sessions) != 1 {
		t.Fatalf("a config edit made a session: %d -> %d files", len(before), len(after))
	}
	var post struct {
		Session string
		N       int
	}
	r.ok("POST", "/agents/"+id+"/records?via=web", "hello", &post)
	if post.Session == s1 {
		t.Fatal("the post stayed on the session of the old model")
	}
	info := r.idle(id)
	if !slices.Equal(info.Sessions, []string{s1, post.Session}) {
		t.Fatalf("sessions %v", info.Sessions)
	}
	c2 := r.chain(post.Session)
	var c statefile.Conf
	must(t, json.Unmarshal(c2[1].Body, &c))
	if c.Model != "fake/c" || !strings.Contains(lastSystem(c2, "continue"), "from session "+s1) {
		t.Fatalf("S2 model %s, continue %q", c.Model, lastSystem(c2, "continue"))
	}
	if lastSystem(r.chain(s1), "continue") != post.Session {
		t.Fatalf("S1 does not name S2")
	}
}

func TestWarmFastpath(t *testing.T) {
	r := newRig(t, t.TempDir())
	id, s1 := r.agent(conf("fake/m", 1000, 0, "w1"), "message")
	r.ok("PUT", "/agents/"+id+"/config?via=web", conf("fake/m", 1000, 0, "w1", "w2"), nil)
	chain := r.chain(s1)
	var c statefile.Conf
	for _, rec := range chain {
		if rec.Kind == statefile.Config {
			must(t, json.Unmarshal(rec.Body, &c))
		}
	}
	if len(c.Workers) != 2 || len(r.info(id).Sessions) != 1 {
		t.Fatalf("workers %v, sessions %v", c.Workers, r.info(id).Sessions)
	}
	r.ok("PUT", "/agents/"+id+"/apps?via=web", `["message","schedule"]`, nil)
	if note := lastSystem(r.chain(s1), "apps"); !strings.Contains(note, "- schedule: ") || len(r.info(id).Sessions) != 1 {
		t.Fatalf("apps note %q", note)
	}
}

func TestWarmVsCold(t *testing.T) {
	r := newRig(t, t.TempDir())
	r.ttl = 1
	r.step = func(int64, []byte) ([]statefile.Record, int) { return []statefile.Record{text("ok")}, 140000 }
	id, s1 := r.agent(conf("fake/m", 250000, 100000))
	r.ok("POST", "/agents/"+id+"/records?via=web", "first", nil)
	r.idle(id)
	var post struct{ Session string }
	r.ok("POST", "/agents/"+id+"/records?via=web", "warm", &post)
	if post.Session != s1 {
		t.Fatalf("a post on a warm cache rolled over to %s", post.Session)
	}
	r.idle(id)
	time.Sleep(1100 * time.Millisecond)
	r.ok("POST", "/agents/"+id+"/records?via=web", "cold", &post)
	if post.Session == s1 {
		t.Fatal("a post on a cold cache over cold_limit stayed")
	}
	if c := lastSystem(r.chain(post.Session), "continue"); !strings.Contains(c, "cold limit") {
		t.Fatalf("continue note %q", c)
	}
}

func TestMidrunLimit(t *testing.T) {
	r := newRig(t, t.TempDir())
	release := make(chan struct{})
	var s1 string
	var s1Tip atomic.Value
	r.step = func(n int64, body []byte) ([]statefile.Record, int) {
		switch n {
		case 1:
			<-release
			return []statefile.Record{{Header: statefile.Header{Kind: statefile.Call, ID: "c1", Type: "application/json"}, Body: []byte(`{"name":"run","arguments":{"worker":"w1","code":"true"}}`)}}, 5000
		case 2: // the next session's first step: the old one is done and says where it went
			chain, _ := r.h.Sessions.Load(s1)
			s1Tip.Store(chain[len(chain)-1].Kind)
			if !strings.Contains(string(body), "late news") {
				s1Tip.Store(statefile.Kind("no late message"))
			}
		}
		return []statefile.Record{text("ok")}, 100
	}
	var id string
	id, s1 = r.agent(conf("fake/m", 1000, 0, "w1"))
	r.ok("POST", "/agents/"+id+"/records?via=web", "go", nil)
	eventually(t, "stepping", func() bool { return r.info(id).Status == statefile.StatusStepping })
	var post struct{ Session string }
	r.ok("POST", "/agents/"+id+"/records?via=web", "late news", &post)
	if post.Session != s1 {
		t.Fatal("a post to a stepping session went elsewhere")
	}
	close(release)
	eventually(t, "a second session that stepped", func() bool { return r.steps.Load() >= 2 })
	info := r.idle(id)
	if len(info.Sessions) != 2 || info.Status != statefile.StatusClosed {
		t.Fatalf("info %+v", info)
	}
	c1 := r.chain(s1)
	tip, prev := c1[len(c1)-1], c1[len(c1)-2]
	if tip.Kind != statefile.Error || prev.Source != "continue" || string(prev.Body) != info.Sessions[1] {
		t.Fatalf("S1 ends %s %s %q", prev.Kind, tip.Kind, tip.Body)
	}
	if k := s1Tip.Load(); k != statefile.Error {
		t.Fatalf("S2's first step saw S1's tip as %v", k)
	}
	c2 := r.chain(info.Sessions[1])
	if late := messages(c2, "web"); !slices.Equal(late, []string{"late news"}) {
		t.Fatalf("carried %v", late)
	}
	if h := messages(c2, "harness"); len(h) != 1 || !strings.Contains(h[0], "hit its context limit mid-task") {
		t.Fatalf("continue message %v", h)
	}
	if statefile.Derive(c1) != statefile.StatusFailed {
		t.Fatal("S1 would be kicked again")
	}
}

func TestSkeyRevoke(t *testing.T) {
	r := newRig(t, t.TempDir())
	id, s1 := r.agent(conf("fake/a", 1000, 0), "echo")
	k1 := r.signer.Sign(auth.SessionKey, s1)
	code, b := r.do(k1, "GET", "/apps/echo/x/y", "")
	if code != 200 || b != fmt.Sprintf("agent=%s session=%s path=/x/y auth=\"\"", id, s1) {
		t.Fatalf("live key: %d %s", code, b)
	}
	if code, b := r.do(k1, "GET", "/skills", ""); code != 200 || !strings.Contains(b, `"echo"`) || strings.Contains(b, "schedule") {
		t.Fatalf("skills: %d %s", code, b)
	}
	for _, p := range []string{"/sessions", "/agents", "/secrets", "/apps"} {
		if code, _ := r.do(k1, "GET", p, ""); code != 403 {
			t.Fatalf("session key on %s: %d", p, code)
		}
	}
	if code, _ := r.do(r.key, "GET", "/apps/echo/x", ""); code != 403 {
		t.Fatalf("api key on an app: %d", code)
	}
	var plain struct{ ID string }
	r.ok("POST", "/sessions", `{"model":"fake/a","context_limit":1000}`, &plain)
	if code, _ := r.do(r.signer.Sign(auth.SessionKey, plain.ID), "GET", "/apps/echo", ""); code != 403 {
		t.Fatalf("plain session's key: %d", code)
	}
	r.ok("PUT", "/agents/"+id+"/apps?via=web", `[]`, nil)
	for _, p := range []string{"/apps/echo", "/skills/echo"} {
		if code, _ := r.do(k1, "GET", p, ""); code != 403 {
			t.Fatalf("app taken away, %s: %d", p, code)
		}
	}
	r.ok("PUT", "/agents/"+id+"/apps?via=web", `["echo"]`, nil)
	r.ok("PUT", "/agents/"+id+"/config?via=web", conf("fake/b", 1000, 0), nil)
	var post struct{ Session string }
	r.ok("POST", "/agents/"+id+"/records?via=web", "next", &post)
	for _, p := range []string{"/apps/echo", "/skills/echo"} {
		if code, _ := r.do(k1, "GET", p, ""); code != 403 {
			t.Fatalf("rolled over, %s: %d", p, code)
		}
	}
	if code, _ := r.do(r.signer.Sign(auth.SessionKey, post.Session), "GET", "/skills/echo", ""); code != 200 {
		t.Fatalf("new session's key: %d", code)
	}
	if code, _ := r.do(k1[:len(k1)-2]+"AA", "GET", "/skills/echo", ""); code != 401 {
		t.Fatalf("forged key: %d", code)
	}
}

// install writes a run app of the test binary into dir/apps/name.
func install(t *testing.T, dir, name, manifest string) {
	d := filepath.Join(dir, "apps", name)
	must(t, os.MkdirAll(d, 0o755))
	must(t, os.WriteFile(filepath.Join(d, "skill.md"), []byte("The "+name+" test app.\n"), 0o644))
	bin, err := os.Executable()
	must(t, err)
	var m map[string]any
	must(t, json.Unmarshal([]byte(manifest), &m))
	if _, ok := m["url"]; !ok {
		m["run"] = []string{bin, "daemon"}
	}
	b, _ := json.Marshal(m)
	must(t, os.WriteFile(filepath.Join(d, "app.json"), b, 0o644))
}

// call is an app call with a's live session key, retried while the daemon
// comes up.
func (r *rig) call(agent, path string) string {
	r.t.Helper()
	key := r.signer.Sign(auth.SessionKey, r.info(agent).CurrentSession)
	var code int
	var b string
	for range 300 {
		if code, b = r.do(key, "GET", path, ""); code != 502 && code != 503 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != 200 {
		r.t.Fatalf("%s: %d %s", path, code, b)
	}
	return b
}

func TestAkeyScopeAndWebhooks(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, "plain", `{}`)
	install(t, dir, "poster", `{"entitlements":["post","list"],"webhooks":true}`)
	r := newRig(t, dir)
	a, _ := r.agent(conf("fake/m", 1000, 0), "plain", "poster")
	b, _ := r.agent(conf("fake/m", 1000, 0), "plain")
	if env := r.call(a, "/apps/plain/env"); strings.Contains(env, "MAINPLANE_APP_KEY") || !strings.Contains(env, `"agent":"`+a+`"`) {
		t.Fatalf("plain env %s", env)
	}
	if env := r.call(a, "/apps/poster/env"); !strings.Contains(env, "mp_akey_") {
		t.Fatalf("poster env %s", env)
	}
	if got := r.call(a, "/apps/poster/post?to="+a); !strings.HasPrefix(got, "200 ") {
		t.Fatalf("post to a holder: %s", got)
	}
	eventually(t, "the app's post", func() bool {
		return slices.Contains(messages(r.chain(r.info(a).CurrentSession), "app:poster"), "from the app")
	})
	if got := r.call(a, "/apps/poster/post?to="+b); !strings.HasPrefix(got, "403 ") {
		t.Fatalf("post to a non-holder: %s", got)
	}
	if got := r.call(a, "/apps/poster/list"); got != fmt.Sprintf("200 [{\"id\":%q,\"name\":\"\"}]\n", a) {
		t.Fatalf("list: %q", got)
	}
	if got := r.call(a, "/apps/poster/sessions"); got != "403" {
		t.Fatalf("app key on /sessions: %s", got)
	}
	resp, err := http.Post(r.url+"/webhooks/poster/gh/events", "application/json", strings.NewReader("{}"))
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `agent="" path=/webhooks/gh/events` {
		t.Fatalf("webhook: %d %s", resp.StatusCode, body)
	}
	if resp, err = http.Post(r.url+"/webhooks/plain/x", "application/json", nil); err != nil || resp.StatusCode != 404 {
		t.Fatalf("webhook to an app without: %v %v", err, resp.StatusCode)
	}
	r.ok("POST", "/apps/poster/stop", "", nil)
	var infos []apps.Info
	r.ok("GET", "/apps", "", &infos)
	if i := slices.IndexFunc(infos, func(i apps.Info) bool { return i.Name == "poster" }); infos[i].Status != apps.Stopped {
		t.Fatalf("stopped app: %+v", infos[i])
	}
	r.ok("POST", "/apps/poster/start", "", nil)
	r.call(a, "/apps/poster/env")
}

func TestSecretIso(t *testing.T) {
	dir := t.TempDir()
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = fmt.Fprintf(w, "key=%s path=%s auth=%q agent=%s", req.Header.Get("X-Api-Key"), req.URL.Path, req.Header.Get("Authorization"), req.Header.Get(apps.AgentHeader))
	}))
	defer vendor.Close()
	install(t, dir, "has", `{"secrets":["K"]}`)
	install(t, dir, "lacks", `{}`)
	install(t, dir, "vendor", `{"url":"`+vendor.URL+`","secrets":["K"],"headers":{"X-Api-Key":"$K"}}`)
	r := newRig(t, dir)
	id, _ := r.agent(conf("fake/m", 1000, 0), "has", "lacks", "vendor")
	key := r.signer.Sign(auth.SessionKey, r.info(id).CurrentSession)
	if code, b := r.do(key, "GET", "/apps/has/env", ""); code != 503 || !strings.Contains(b, "missing secrets: K") {
		t.Fatalf("before the secret: %d %s", code, b)
	}
	r.ok("PUT", "/secrets/K", "s3cret=with\nlines", nil)
	if _, b := r.do(r.key, "GET", "/secrets", ""); strings.TrimSpace(b) != `["K"]` {
		t.Fatalf("GET /secrets %s", b)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".secrets")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".secrets: %v %v", fi.Mode(), err)
	}
	if env := r.call(id, "/apps/has/env"); !strings.Contains(env, `"K":"s3cret=with\nlines"`) {
		t.Fatalf("has: %s", env)
	}
	if env := r.call(id, "/apps/lacks/env"); strings.Contains(env, `"K"`) {
		t.Fatalf("lacks: %s", env)
	}
	r.ok("PUT", "/secrets/K", "s3cret=one-line", nil) // a header value takes no newline
	if got := r.call(id, "/apps/vendor/v1/search"); got != `key=s3cret=one-line path=/v1/search auth="" agent=`+id {
		t.Fatalf("vendor: %q", got)
	}
	r.ok("DELETE", "/secrets/K", "", nil)
	if _, b := r.do(r.key, "GET", "/secrets", ""); strings.TrimSpace(b) != `[]` {
		t.Fatalf("after delete %s", b)
	}
}

func TestOrchestrate(t *testing.T) {
	r := newRig(t, t.TempDir())
	parent, ps := r.agent(conf("fake/m", 1000, 0, "w1"), "orchestrate", "message")
	key := r.signer.Sign(auth.SessionKey, ps)
	spawn := func(body string) (int, string) { return r.do(key, "POST", "/apps/orchestrate/spawn", body) }
	if code, b := spawn(`{"config":` + conf("fake/m", 1000, 0, "w1", "w2") + `}`); code != 403 {
		t.Fatalf("more workers: %d %s", code, b)
	}
	if code, b := spawn(`{"apps":["schedule"]}`); code != 403 {
		t.Fatalf("more apps: %d %s", code, b)
	}
	code, b := spawn(`{"name":"quiet","apps":["message"],"task":"work"}`)
	var quiet struct{ ID string }
	if code != 200 || json.Unmarshal([]byte(b), &quiet) != nil {
		t.Fatalf("spawn: %d %s", code, b)
	}
	if info := r.idle(quiet.ID); info.Via != "agent:"+parent || info.Config.Workers[0].Name != "w1" {
		t.Fatalf("child %+v", info)
	}
	_, b = spawn(`{"name":"loud","task":"work","notify_on_stop":true}`)
	var loud struct{ ID string }
	must(t, json.Unmarshal([]byte(b), &loud))
	r.idle(loud.ID)
	notices := func() []string { return messages(r.chain(r.info(parent).CurrentSession), "app:orchestrate") }
	eventually(t, "one notice", func() bool { return len(notices()) == 1 })
	if n := notices(); !strings.Contains(n[0], loud.ID) {
		t.Fatalf("notice %q", n[0])
	}
	if code, b := r.do(key, "POST", "/apps/message", `{"to":"`+loud.ID+`","text":"more"}`); code != 200 {
		t.Fatalf("message: %d %s", code, b)
	}
	eventually(t, "the child read it", func() bool {
		return slices.Contains(messages(r.chain(r.info(loud.ID).CurrentSession), "agent:"+parent), "more") && r.info(loud.ID).Status == statefile.StatusClosed
	})
	r.idle(parent)
	time.Sleep(100 * time.Millisecond)
	if n := notices(); len(n) != 1 {
		t.Fatalf("notices %v", n)
	}
	var kids []harness.AgentInfo
	_, b = r.do(key, "GET", "/apps/orchestrate/agents", "")
	must(t, json.Unmarshal([]byte(b), &kids))
	if len(kids) != 2 {
		t.Fatalf("children %d: %s", len(kids), b)
	}
	if code, b := r.do(key, "GET", "/apps/orchestrate/agents/"+parent, ""); code != 404 {
		t.Fatalf("not a child: %d %s", code, b)
	}
	if code, b := r.do(key, "GET", "/apps/orchestrate/agents/"+quiet.ID+"/records", ""); code != 200 || !strings.Contains(b, `"via":"agent:`+parent+`"`) {
		t.Fatalf("records: %d", code)
	}
	if code, b := r.do(key, "PUT", "/apps/orchestrate/agents/"+quiet.ID, `{"apps":["orchestrate","schedule"]}`); code != 403 {
		t.Fatalf("update past the caller: %d %s", code, b)
	}
	if code, _ := r.do(key, "DELETE", "/apps/orchestrate/agents/"+quiet.ID, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := r.do(r.key, "GET", "/agents/"+quiet.ID, ""); code != 400 {
		t.Fatalf("deleted child: %d", code)
	}
}

func TestSchedule(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	id, sid := r.agent(conf("fake/m", 1000, 0), "schedule")
	key := r.signer.Sign(auth.SessionKey, sid)
	if code, b := r.do(key, "POST", "/apps/schedule", `{"text":"soon","in":"200ms"}`); code != 200 {
		t.Fatalf("set: %d %s", code, b)
	}
	if code, b := r.do(key, "POST", "/apps/schedule", `{"text":"daily","cron":"0 9 * * *"}`); code != 200 {
		t.Fatalf("cron: %d %s", code, b)
	}
	if code, _ := r.do(key, "POST", "/apps/schedule", `{"text":"x","cron":"0 0 31 2 *"}`); code != 400 {
		t.Fatalf("a cron that never fires: %d", code)
	}
	eventually(t, "the timer fired", func() bool {
		return len(messages(r.chain(r.info(id).CurrentSession), "app:schedule")) == 1
	})
	var timers []struct{ ID, Cron string }
	_, b := r.do(key, "GET", "/apps/schedule", "")
	must(t, json.Unmarshal([]byte(b), &timers))
	if len(timers) != 1 || timers[0].Cron != "0 9 * * *" {
		t.Fatalf("timers after firing: %s", b)
	}
	// A timer due while the harness was down fires when it starts again.
	r.idle(id)
	code, set := r.do(key, "POST", "/apps/schedule", `{"text":"after restart","in":"1s"}`)
	var later timerReply
	if code != 200 || json.Unmarshal([]byte(set), &later) != nil {
		t.Fatalf("set: %d %s", code, set)
	}
	r.stop()
	if !time.Now().Before(later.Next) {
		t.Fatal("the harness stopped after the timer was due")
	}
	time.Sleep(time.Until(later.Next) + 100*time.Millisecond)
	if n := len(messages(r.chain(sid), "app:schedule")); n != 1 {
		t.Fatalf("%d timer messages before the restart", n)
	}
	restarted := newRig(t, dir)
	eventually(t, "the timer fired after a restart", func() bool {
		ms := messages(restarted.chain(restarted.info(id).CurrentSession), "app:schedule")
		return len(ms) == 2 && strings.HasSuffix(ms[1], " fired: after restart")
	})
}

// timerReply is a timer as the schedule app returns it.
type timerReply struct{ Next time.Time }

func TestScheduleRetry(t *testing.T) {
	r := newRig(t, t.TempDir())
	id, sid := r.agent(conf("fake/m", 1000, 0), "schedule")
	key := r.signer.Sign(auth.SessionKey, sid)
	path := r.h.Sessions.Path(sid)
	must(t, os.Rename(path, path+".away")) // the post cannot load the session
	if code, b := r.do(key, "POST", "/apps/schedule", `{"text":"again","in":"100ms"}`); code != 200 {
		t.Fatalf("set: %d %s", code, b)
	}
	time.Sleep(500 * time.Millisecond)
	if _, b := r.do(key, "GET", "/apps/schedule", ""); !strings.Contains(b, `"again"`) {
		t.Fatalf("a failed timer was dropped: %s", b)
	}
	must(t, os.Rename(path+".away", path))
	for range 100 {
		if len(messages(r.chain(r.info(id).CurrentSession), "app:schedule")) == 1 {
			if _, b := r.do(key, "GET", "/apps/schedule", ""); strings.TrimSpace(b) != "[]" {
				t.Fatalf("a timer that landed stayed: %s", b)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the failed timer never landed")
}

func TestMidrunConfig(t *testing.T) {
	r := newRig(t, t.TempDir())
	release := make(chan struct{})
	r.step = func(n int64, body []byte) ([]statefile.Record, int) {
		if n == 1 {
			<-release
			return []statefile.Record{{Header: statefile.Header{Kind: statefile.Call, ID: "c1", Type: "application/json"}, Body: []byte(`{"name":"run","arguments":{"worker":"w1","code":"true"}}`)}}, 100
		}
		return []statefile.Record{text("ok")}, 100
	}
	id, s1 := r.agent(conf("fake/a", 1000, 0, "w1"))
	r.ok("POST", "/agents/"+id+"/records?via=web", "go", nil)
	eventually(t, "stepping", func() bool { return r.info(id).Status == statefile.StatusStepping })
	r.ok("PUT", "/agents/"+id+"/config?via=web", conf("fake/b", 1000, 0, "w1"), nil)
	close(release)
	eventually(t, "a second session", func() bool { return len(r.info(id).Sessions) == 2 })
	info := r.idle(id)
	c2 := r.chain(info.Sessions[1])
	if h := messages(c2, "harness"); len(h) != 1 || !strings.Contains(h[0], "Session "+s1+" stopped mid-task because the agent's config changed") {
		t.Fatalf("continue message %v", h)
	}
	if c := lastSystem(c2, "continue"); !strings.Contains(c, "which stopped mid-task because the agent's config changed") {
		t.Fatalf("continue note %q", c)
	}
}

func TestAgentEdges(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, "opaque", `{"url":"http:vendor"}`)
	install(t, dir, "forger", `{"url":"http://vendor.example","headers":{"x-mainplane-agent":"a1"}}`)
	r := newRig(t, dir)
	if _, b := r.do(r.key, "GET", "/apps", ""); strings.Contains(b, "opaque") || strings.Contains(b, "forger") {
		t.Fatalf("a bad app.json loaded: %s", b)
	}
	if code, b := r.do(r.key, "POST", "/agents?via=web", `{"session":"../../x"}`); code != 400 || !strings.Contains(b, "bad session id") {
		t.Fatalf("adopt a path: %d %s", code, b)
	}
	id, sid := r.agent(conf("fake/m", 1000, 0))
	for _, c := range []string{`{"model":"fake/m","context_limit":1000,"tools":"bogus"}`, `{"model":"fake/m","context_limit":1000,"input":["application/x-nope"]}`} {
		if code, b := r.do(r.key, "PUT", "/agents/"+id+"/config?via=web", c); code != 400 {
			t.Fatalf("config %s: %d %s", c, code, b)
		}
	}
	if info := r.info(id); info.Config.Tools != "default" || info.Config.Input != nil {
		t.Fatalf("a refused config was kept: %+v", info.Config)
	}
	if code, b := r.do(r.key, "DELETE", "/sessions/"+sid+"?via=web", ""); code != 400 || !strings.Contains(b, "live session of agent "+id) {
		t.Fatalf("delete a live session: %d %s", code, b)
	}
	// A delete waits for the step it stops; the session is then no agent's.
	r.step = func(int64, []byte) ([]statefile.Record, int) {
		time.Sleep(300 * time.Millisecond)
		return []statefile.Record{text("ok")}, 100
	}
	r.ok("POST", "/agents/"+id+"/records?via=web", "slow", nil)
	eventually(t, "stepping", func() bool { return r.info(id).Status == statefile.StatusStepping })
	r.ok("DELETE", "/agents/"+id+"?via=web", "", nil)
	var si struct{ Status statefile.Status }
	r.ok("GET", "/sessions/"+sid, "", &si)
	if si.Status == statefile.StatusStepping {
		t.Fatal("a deleted agent's session still steps")
	}
	if code, _ := r.do(r.signer.Sign(auth.SessionKey, sid), "GET", "/skills", ""); code != 403 {
		t.Fatalf("a deleted agent's key: %d", code)
	}
	r.ok("DELETE", "/sessions/"+sid+"?via=web", "", nil)
	// Secrets: names the harness sets in a run app are refused; a null file is empty.
	for _, n := range []string{"PORT", "DATA_DIR", "MAINPLANE_APP_KEY"} {
		if code, _ := r.do(r.key, "PUT", "/secrets/"+n, "x"); code != 400 {
			t.Fatalf("secret %s: %d", n, code)
		}
	}
	must(t, os.WriteFile(filepath.Join(dir, ".secrets"), []byte("null"), 0o600))
	r.ok("PUT", "/secrets/K", "v", nil)
}

func TestAdoptWhileRenaming(t *testing.T) {
	r := newRig(t, t.TempDir())
	id, _ := r.agent(conf("fake/m", 1000, 0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 30 {
			r.ok("PUT", "/agents/"+id+"/name?via=web", fmt.Sprintf("n%d", i), nil)
		}
	}()
	for range 30 {
		var s struct{ ID string }
		r.ok("POST", "/sessions?via=tui", `{"model":"fake/m","context_limit":1000}`, &s)
		r.ok("POST", "/agents?via=web", `{"session":"`+s.ID+`"}`, nil)
	}
	<-done
}

func TestSpawnUndo(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	parent, ps := r.agent(conf("fake/m", 1000, 0), "orchestrate")
	must(t, os.MkdirAll(filepath.Join(dir, "app-data", "orchestrate", "watch.json.tmp"), 0o755)) // the watch cannot save
	key := r.signer.Sign(auth.SessionKey, ps)
	if code, b := r.do(key, "POST", "/apps/orchestrate/spawn", `{"task":"work","notify_on_stop":true}`); code == 200 {
		t.Fatalf("spawn: %d %s", code, b)
	}
	var all []harness.AgentInfo
	r.ok("GET", "/agents", "", &all)
	if len(all) != 1 || all[0].ID != parent {
		t.Fatalf("a failed spawn left an agent: %+v", all)
	}
}
