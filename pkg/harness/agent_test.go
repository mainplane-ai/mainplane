package harness_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
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
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

type testEnvelope struct {
	tag string
}

func (e testEnvelope) Name() string        { return "anthropic" }
func (e testEnvelope) Accepts(string) bool { return true }
func (e testEnvelope) Owns(string) bool    { return false }
func (e testEnvelope) Compile(req provider.Request) ([]byte, provider.Cache, error) {
	b, err := json.Marshal(struct {
		Tag   string             `json:"tag"`
		Model string             `json:"model"`
		Key   string             `json:"key"`
		N     int                `json:"n"`
		Ctx   []statefile.Record `json:"ctx"`
	}{Tag: e.tag, Model: req.Model, Key: req.Key, N: len(req.Context), Ctx: req.Context})
	return b, provider.Cache{TTL: 2}, err
}

func (e testEnvelope) Stream(io.Reader, *provider.Cache, func(statefile.Record)) (statefile.Header, error) {
	return statefile.Header{}, nil
}

type stepFunc func(ctx context.Context, model, sessionKey string, recs []statefile.Record, cache *provider.Cache, emit func(statefile.Record)) (statefile.Header, error)

type fakeWorker struct {
	name    string
	scratch string
	mu      sync.Mutex
	files   map[string][]byte
	onRun   func(f worker.Frame) string
}

func startFakeWorker(t *testing.T, pool *harness.Pool, name string) *fakeWorker {
	t.Helper()
	scratch := t.TempDir()
	fw := &fakeWorker{name: name, scratch: scratch, files: map[string][]byte{}}
	c1, c2 := net.Pipe()
	t.Cleanup(func() {
		_ = c1.Close()
		_ = c2.Close()
	})
	go fw.serve(c2)
	r, err := harness.Connect(c1, func() (string, error) { return name, nil })
	if err != nil {
		t.Fatalf("connect fake worker: %v", err)
	}
	pool.Add(r)
	return fw
}

func (fw *fakeWorker) serve(conn net.Conn) {
	_ = worker.Encode(conn, worker.Frame{Header: worker.Header{
		Kind:    worker.Hello,
		OS:      "linux",
		Arch:    "amd64",
		Interps: []string{"bash"},
		Scratch: fw.scratch,
		Version: version.V,
	}})
	br := bufio.NewReader(conn)
	for {
		f, err := worker.Decode(br)
		if err != nil {
			return
		}
		fw.handleFrame(conn, f)
	}
}

func (fw *fakeWorker) handleFrame(conn net.Conn, f worker.Frame) {
	switch f.Kind {
	case worker.Drives:
	case worker.Write:
		fw.mu.Lock()
		fw.files[f.Path] = append([]byte(nil), f.Body...)
		fw.mu.Unlock()
		_ = os.MkdirAll(filepath.Dir(f.Path), 0o755)
		_ = os.WriteFile(f.Path, f.Body, 0o600)
		_ = worker.Encode(conn, worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Done}})
	case worker.Read:
		b, err := os.ReadFile(f.Path)
		if err != nil {
			_ = worker.Encode(conn, worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Error}, Body: []byte(err.Error())})
			return
		}
		_ = worker.Encode(conn, worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Bytes}, Body: b})
	case worker.Kill:
		_ = worker.Encode(conn, worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Done}})
	case worker.Run:
		out := "ok\n"
		fw.mu.Lock()
		fn := fw.onRun
		fw.mu.Unlock()
		if fn != nil {
			out = fn(f)
		}
		_ = worker.Encode(conn, worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Output}, Body: []byte(out)})
		_ = worker.Encode(conn, worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Result, Exit: 0}})
	}
}

func (fw *fakeWorker) hasFile(p string) bool {
	fw.mu.Lock()
	_, ok := fw.files[p]
	fw.mu.Unlock()
	return ok
}

type testRig struct {
	dir     string
	h       *harness.Harness
	mgr     *apps.Manager
	pool    *harness.Pool
	srv     *httptest.Server
	cli     *connector.Client
	priv    ed25519.PrivateKey
	hkey    string
	fullKey string
	calls   atomic.Int64
	mu      sync.Mutex
	onStep  stepFunc
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hkey := "rigkey"
	store := auth.Store{Path: filepath.Join(dir, "auth.json")}
	fullTok, err := store.Issue(auth.Key, "admin")
	if err != nil {
		t.Fatal(err)
	}
	rig := &testRig{dir: dir, priv: priv, hkey: hkey, fullKey: fullTok}
	var srvURL string
	pool := harness.NewPool(func(string) worker.Desired { return worker.Desired{} })
	pool.Env = func(sid string) (string, string) {
		return auth.SessionToken(priv, hkey, sid), srvURL
	}
	rig.pool = pool

	prov := rig.makeProvider("v1")
	h := &harness.Harness{
		Sessions:  statefile.Sessions{Dir: filepath.Join(dir, "sessions")},
		AgentsDir: filepath.Join(dir, "agents"),
		Providers: map[string]provider.Provider{"anthropic": prov, "openai": prov},
		Workers:   pool,
	}
	mgr, err := apps.New(apps.Config{
		Dir:         dir,
		Host:        h,
		LoopbackURL: func() string { return srvURL },
		AppToken:    func(app string) string { return auth.AppToken(priv, hkey, app) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Apps = mgr
	rig.h, rig.mgr = h, mgr

	ctx, cancel := context.WithCancel(context.Background())
	routes := http.NewServeMux()
	routes.Handle("/", harness.Handler(ctx, h))
	mux := http.NewServeMux()
	mgr.RegisterWebhooks(mux)
	mux.Handle("/", store.Gate(priv, hkey, &auth.Limit{}, routes))
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	rig.srv = srv
	rig.cli = &connector.Client{URL: srv.URL, Pinned: true, Key: fullTok, Local: srv.URL}
	if err := h.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	mgr.Start(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})
	return rig
}

func (rig *testRig) makeProvider(tag string) provider.Provider {
	return provider.Provider{
		Envelope: testEnvelope{tag: tag},
		Run: func(ctx context.Context, body []byte, cache *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
			rig.calls.Add(1)
			var req struct {
				Model string             `json:"model"`
				Key   string             `json:"key"`
				Ctx   []statefile.Record `json:"ctx"`
			}
			_ = json.Unmarshal(body, &req)
			rig.mu.Lock()
			fn := rig.onStep
			rig.mu.Unlock()
			if fn != nil {
				return fn(ctx, req.Model, req.Key, req.Ctx, cache, emit)
			}
			emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("done")})
			return statefile.Header{
				Model: req.Model,
				Stop:  "end_turn",
				Usage: &statefile.Usage{Input: 100, CacheRead: 50, Output: 20},
			}, nil
		},
	}
}

func (rig *testRig) setStep(fn stepFunc) {
	rig.mu.Lock()
	rig.onStep = fn
	rig.mu.Unlock()
}

func (rig *testRig) waitAgentIdle(t *testing.T, id string) harness.AgentInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		in, err := rig.cli.Agent(context.Background(), id)
		if err == nil && (in.Status == statefile.StatusClosed || in.Status == statefile.StatusFailed) {
			return in
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for agent %s to finish stepping", id)
	return harness.AgentInfo{}
}

func defaultAgentCfg(workers ...string) *harness.AgentConfig {
	ws := make([]statefile.Worker, 0, len(workers))
	for _, w := range workers {
		ws = append(ws, statefile.Worker{Name: w})
	}
	return &harness.AgentConfig{
		Model:        "anthropic/claude-opus-4-6",
		Tools:        "default",
		Workers:      ws,
		ContextLimit: 250000,
		ColdLimit:    100000,
	}
}

// Covers: create-1trip, create-2trip, via-start.
func TestConformanceCreateAndVia(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	// 1. create-1trip: creates S1 and agents/<id>.json, zero LLM calls, status=closed.
	aid, s1, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Name:   "proj",
		Config: defaultAgentCfg(),
		Apps:   []string{"schedule", "message"},
	}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if rig.calls.Load() != 0 {
		t.Fatalf("create-1trip made %d LLM calls, want 0", rig.calls.Load())
	}
	info, err := rig.cli.Agent(ctx, aid)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != statefile.StatusClosed || info.CurrentSession != s1 || len(info.Sessions) != 1 {
		t.Fatalf("unexpected 1-trip info: %+v", info)
	}
	if _, err := os.Stat(filepath.Join(rig.dir, "agents", aid+".json")); err != nil {
		t.Fatalf("missing agents/%s.json: %v", aid, err)
	}

	// 2. via-start: TUI session has via="tui" on Start; agent session has via="agent:<aid>"; Build excludes Start.
	tuiSid, err := rig.cli.Create(ctx, harness.Create{
		Model:        "anthropic/claude-opus-4-6",
		Tools:        "default",
		ContextLimit: 200000,
	}, "tui")
	if err != nil {
		t.Fatal(err)
	}
	tuiChain, err := rig.h.Sessions.Load(tuiSid)
	if err != nil || tuiChain[0].Via != "tui" {
		t.Fatalf("tui start via=%q err=%v", tuiChain[0].Via, err)
	}
	agChain, err := rig.h.Sessions.Load(s1)
	if err != nil || agChain[0].Via != "agent:"+aid {
		t.Fatalf("agent start via=%q, want agent:%s", agChain[0].Via, aid)
	}
	built, _ := statefile.Build(agChain)
	for _, r := range built {
		if r.Kind == statefile.Start {
			t.Fatal("statefile.Build included Start record")
		}
	}

	// 3. create-2trip: adopt tuiSid, derive config, append apps note, refuse second agent claiming tuiSid.
	aid2, adopted, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Name:    "adopted",
		Session: tuiSid,
		Apps:    []string{"orchestrate"},
	}, "web")
	if err != nil || adopted != tuiSid {
		t.Fatalf("create-2trip: id=%s sid=%s err=%v", aid2, adopted, err)
	}
	in2, err := rig.cli.Agent(ctx, aid2)
	if err != nil || in2.Config.Model != "anthropic/claude-opus-4-6" {
		t.Fatalf("2-trip derived config wrong: %+v err=%v", in2, err)
	}
	adoptedChain, _ := rig.h.Sessions.Load(tuiSid)
	if !slices.ContainsFunc(adoptedChain, func(r statefile.Record) bool { return r.Kind == statefile.System && r.Source == "apps" }) {
		t.Fatal("2-trip did not append apps system note")
	}
	if _, _, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{Session: tuiSid}, "web"); err == nil {
		t.Fatal("expected second agent claiming S1 to be refused")
	}
}

// Covers: lazy-config, warm-fastpath.
func TestConformanceLazyAndWarmFastpath(t *testing.T) {
	rig := newTestRig(t)
	startFakeWorker(t, rig.pool, "w1")
	startFakeWorker(t, rig.pool, "w2")
	ctx := context.Background()

	aid, s1, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Name:   "lazy",
		Config: defaultAgentCfg("w1"),
		Apps:   []string{"message"},
	}, "web")
	if err != nil {
		t.Fatal(err)
	}

	// warm-fastpath: changing ONLY workers updates S1 in place with no rollover; changing apps appends apps note in place.
	cfgW := *defaultAgentCfg("w1", "w2")
	if err := rig.cli.SetAgentConfig(ctx, aid, "web", cfgW); err != nil {
		t.Fatal(err)
	}
	if err := rig.cli.SetAgentApps(ctx, aid, "web", []string{"message", "schedule"}); err != nil {
		t.Fatal(err)
	}
	in, _ := rig.cli.Agent(ctx, aid)
	if len(in.Sessions) != 1 || in.CurrentSession != s1 {
		t.Fatalf("warm-fastpath rolled over unexpectedly: %+v", in)
	}
	s1Info, _ := rig.cli.Info(ctx, s1)
	if len(s1Info.Config.Workers) != 2 {
		t.Fatalf("S1 workers not updated in place: %+v", s1Info.Config.Workers)
	}
	chain, _ := rig.h.Sessions.Load(s1)
	if !slices.ContainsFunc(chain, func(r statefile.Record) bool {
		return r.Kind == statefile.System && r.Source == "apps" && strings.Contains(string(r.Body), "schedule")
	}) {
		t.Fatal("PUT /apps did not append updated apps system note to S1")
	}

	// Post once on S1 so S1 has history, then change model twice lazily -> still 1 session until next post.
	if _, _, err := rig.cli.PostAgent(ctx, aid, "web", "first turn", nil); err != nil {
		t.Fatal(err)
	}
	rig.waitAgentIdle(t, aid)

	cfgM1 := cfgW
	cfgM1.Model = "anthropic/claude-sonnet-4-6"
	_ = rig.cli.SetAgentConfig(ctx, aid, "web", cfgM1)
	cfgM2 := cfgW
	cfgM2.Model = "openai/gpt-5.4"
	if err := rig.cli.SetAgentConfig(ctx, aid, "web", cfgM2); err != nil {
		t.Fatal(err)
	}
	in, _ = rig.cli.Agent(ctx, aid)
	if len(in.Sessions) != 1 {
		t.Fatalf("lazy-config created session prematurely: %+v", in.Sessions)
	}

	// Next post triggers Rule 2 rollover -> S2 with openai/gpt-5.4 and continue note.
	s2, _, err := rig.cli.PostAgent(ctx, aid, "web", "second turn", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s2 == s1 {
		t.Fatal("expected rollover to S2 after model change")
	}
	rig.waitAgentIdle(t, aid)
	checkLazyRolloverRecords(t, rig, s1, s2)
}

func checkLazyRolloverRecords(t *testing.T, rig *testRig, s1, s2 string) {
	t.Helper()
	c1, _ := rig.h.Sessions.Load(s1)
	if !slices.ContainsFunc(c1, func(r statefile.Record) bool {
		return r.Kind == statefile.System && r.Source == "continue" && strings.TrimSpace(string(r.Body)) == s2
	}) {
		t.Fatalf("S1 missing system(continue, %s)", s2)
	}
	c2, _ := rig.h.Sessions.Load(s2)
	s2Info, _ := rig.cli.Info(context.Background(), s2)
	if s2Info.Config.Model != "openai/gpt-5.4" {
		t.Fatalf("S2 model=%s, want openai/gpt-5.4", s2Info.Config.Model)
	}
	if !slices.ContainsFunc(c2, func(r statefile.Record) bool {
		return r.Kind == statefile.System && r.Source == "continue" && strings.Contains(string(r.Body), s1)
	}) {
		t.Fatalf("S2 missing continue note referencing %s", s1)
	}
}

// Covers: warm-vs-cold (plus harness rebuild detection).
func TestConformanceWarmVsCold(t *testing.T) {
	rig := newTestRig(t)
	fw := startFakeWorker(t, rig.pool, "w1")
	ctx := context.Background()

	// Step reports 140k tokens (context_limit 250k, cold_limit 100k, cache TTL 2s).
	rig.setStep(func(_ context.Context, model, _ string, _ []statefile.Record, cache *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
		cache.TTL = 2
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("ok")})
		return statefile.Header{
			Model: model,
			Stop:  "end_turn",
			Usage: &statefile.Usage{Input: 40000, CacheRead: 100000, Output: 500},
		}, nil
	})

	aid, s1, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Config: defaultAgentCfg("w1"),
	}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rig.cli.PostAgent(ctx, aid, "web", "turn 1", nil); err != nil {
		t.Fatal(err)
	}
	rig.waitAgentIdle(t, aid)

	// Immediate follow-up while cache is warm (< 2s) and 140.5k < 250k -> stays on S1!
	sidWarm, _, err := rig.cli.PostAgent(ctx, aid, "web", "warm follow-up", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sidWarm != s1 {
		t.Fatalf("warm post rolled over to %s, expected to stay on %s", sidWarm, s1)
	}
	rig.waitAgentIdle(t, aid)

	// Wait for 2s cache TTL to expire -> cache is cold and 140.5k >= 100k cold_limit -> rolls over to S2!
	time.Sleep(2200 * time.Millisecond)
	sidCold, _, err := rig.cli.PostAgent(ctx, aid, "web", "cold follow-up", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sidCold == s1 {
		t.Fatal("cold post above cold_limit stayed on S1; expected rollover to S2")
	}
	rig.waitAgentIdle(t, aid)
	if !fw.hasFile(filepath.Join(fw.scratch, "logs", s1+".log")) {
		t.Fatalf("worker missing copied log for %s after rollover", s1)
	}
}

// Covers: midrun-limit (tool call completes, S1 writes continue + Error, S1.log on worker BEFORE S2 steps, mid-step message copied to S2).
func TestConformanceMidRunRollover(t *testing.T) {
	rig := newTestRig(t)
	fw := startFakeWorker(t, rig.pool, "w1")
	ctx := context.Background()

	cfg := defaultAgentCfg("w1")
	cfg.ContextLimit = 50000
	aid, s1, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{Config: cfg}, "web")
	if err != nil {
		t.Fatal(err)
	}

	// While S1's tool call runs on fw, post an unaddressed mid-step user message to the agent!
	var postedMid atomic.Bool
	fw.onRun = func(_ worker.Frame) string {
		if postedMid.CompareAndSwap(false, true) {
			_, _, _ = rig.cli.PostAgent(ctx, aid, "web", "mid-step user note", nil)
		}
		return "tool-output-from-s1\n"
	}

	var logPresentAtS2Step atomic.Bool
	var stepCount atomic.Int32
	rig.setStep(func(_ context.Context, model, sessionKey string, _ []statefile.Record, _ *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
		n := stepCount.Add(1)
		if n == 1 {
			callBody, _ := json.Marshal(map[string]any{
				"name":      "run",
				"arguments": map[string]any{"worker": "w1", "code": "echo hi"},
			})
			emit(statefile.Record{Header: statefile.Header{Kind: statefile.Call, Type: "application/json"}, Body: callBody})
			return statefile.Header{
				Model: model,
				Stop:  "tool_use",
				Usage: &statefile.Usage{Input: 55000, Output: 1000}, // 56k > 50k context_limit!
			}, nil
		}
		// Step 2 MUST run on S2, and <scratch>/logs/<S1>.log MUST already exist on w1 right now!
		if sessionKey != s1 && fw.hasFile(filepath.Join(fw.scratch, "logs", s1+".log")) {
			logPresentAtS2Step.Store(true)
		}
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("continued in S2")})
		return statefile.Header{
			Model: model,
			Stop:  "end_turn",
			Usage: &statefile.Usage{Input: 2000, Output: 100},
		}, nil
	})

	if _, _, err := rig.cli.PostAgent(ctx, aid, "web", "start big task", nil); err != nil {
		t.Fatal(err)
	}
	in := rig.waitAgentIdle(t, aid)
	verifyMidRunRollover(t, rig, in, s1, logPresentAtS2Step.Load())
}

func verifyMidRunRollover(t *testing.T, rig *testRig, in harness.AgentInfo, s1 string, logWasReady bool) {
	t.Helper()
	if len(in.Sessions) != 2 || in.CurrentSession == s1 || in.Status != statefile.StatusClosed {
		t.Fatalf("expected 2 sessions ending closed on S2, got %+v", in)
	}
	if !logWasReady {
		t.Fatalf("worker did not have <scratch>/logs/%s.log before S2's first step ran", s1)
	}
	s2 := in.CurrentSession
	c1, _ := rig.h.Sessions.Load(s1)
	// S1 must end with Error so Derive(S1) == StatusFailed (Resume will never re-kick S1) and have Result + system("continue", S2).
	if statefile.Derive(c1) != statefile.StatusFailed || c1[len(c1)-1].Kind != statefile.Error {
		t.Fatalf("S1 tip should be Error (StatusFailed), got status=%s tip=%s", statefile.Derive(c1), c1[len(c1)-1].Kind)
	}
	if !slices.ContainsFunc(c1, func(r statefile.Record) bool { return r.Kind == statefile.Result }) {
		t.Fatal("S1 did not record tool Result before rolling over")
	}
	c2, _ := rig.h.Sessions.Load(s2)
	if !slices.ContainsFunc(c2, func(r statefile.Record) bool {
		return r.Kind == statefile.Message && string(r.Body) == "mid-step user note"
	}) {
		t.Fatal("unaddressed mid-step user message was not copied to S2")
	}
}

// Covers: skey-revoke, akey-scope.
func TestConformanceAuthScopes(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	// Write a disk app with entitlements=["post","list"] to verify akey-scope.
	appDir := filepath.Join(rig.dir, "apps", "ghapp")
	_ = os.MkdirAll(appDir, 0o755)
	_ = os.WriteFile(filepath.Join(appDir, "skill.md"), []byte("GitHub integration\n"), 0o644)
	_ = os.WriteFile(filepath.Join(appDir, "app.json"), []byte(`{"entitlements":["post","list"]}`), 0o644)

	aid, s1, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Name:   "auth-agent",
		Config: defaultAgentCfg(),
		Apps:   []string{"schedule", "ghapp"},
	}, "web")
	if err != nil {
		t.Fatal(err)
	}
	otherID, _, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Name:   "other-agent",
		Config: defaultAgentCfg(),
		Apps:   []string{"schedule"},
	}, "web")
	if err != nil {
		t.Fatal(err)
	}

	skey1 := auth.SessionToken(rig.priv, rig.hkey, s1)
	scli1 := &connector.Client{URL: rig.srv.URL, Pinned: true, Key: skey1, Local: rig.srv.URL}

	// 1. skey-revoke: S1 key works on /skills/schedule and /apps/schedule while live, 403 on /secrets and /agents.
	if _, err := scli1.Skill(ctx, "schedule"); err != nil {
		t.Fatalf("live skey failed Skill(schedule): %v", err)
	}
	if _, err := scli1.CallApp(ctx, "schedule", "GET", "/", "", nil); err != nil {
		t.Fatalf("live skey failed CallApp(schedule): %v", err)
	}
	if _, err := scli1.Secrets(ctx); err == nil {
		t.Fatal("skey must get 403 on /secrets")
	}

	// Removing "schedule" from A.apps immediately revokes S1's access to /apps/schedule and /skills/schedule.
	_ = rig.cli.SetAgentApps(ctx, aid, "web", []string{"ghapp"})
	if _, err := scli1.Skill(ctx, "schedule"); err == nil {
		t.Fatal("expected 403 on /skills/schedule after removing schedule from A.apps")
	}
	if _, err := scli1.CallApp(ctx, "schedule", "GET", "/", "", nil); err == nil {
		t.Fatal("expected 403 on /apps/schedule after removing schedule from A.apps")
	}

	// Restore schedule, roll A over to S2 -> S1's skey is immediately revoked (403), S2's skey works!
	_ = rig.cli.SetAgentApps(ctx, aid, "web", []string{"schedule", "ghapp"})
	cfg2 := *defaultAgentCfg()
	cfg2.Model = "openai/gpt-5.4"
	_ = rig.cli.SetAgentConfig(ctx, aid, "web", cfg2)
	s2, _, err := rig.cli.PostAgent(ctx, aid, "web", "roll to s2", nil)
	if err != nil || s2 == s1 {
		t.Fatalf("rollover failed: s2=%s err=%v", s2, err)
	}
	rig.waitAgentIdle(t, aid)
	if _, err := scli1.CallApp(ctx, "schedule", "GET", "/", "", nil); err == nil {
		t.Fatal("rolled-over S1 skey must get 403 on /apps/schedule")
	}
	scli2 := &connector.Client{URL: rig.srv.URL, Pinned: true, Key: auth.SessionToken(rig.priv, rig.hkey, s2), Local: rig.srv.URL}
	if _, err := scli2.CallApp(ctx, "schedule", "GET", "/", "", nil); err != nil {
		t.Fatalf("S2 skey failed on /apps/schedule: %v", err)
	}

	verifyAkeyScope(t, rig, aid, otherID)
}

func verifyAkeyScope(t *testing.T, rig *testRig, aid, otherID string) {
	t.Helper()
	ctx := context.Background()
	akey := auth.AppToken(rig.priv, rig.hkey, "ghapp")
	acli := &connector.Client{URL: rig.srv.URL, Pinned: true, Key: akey, Local: rig.srv.URL}

	// GET /agents with akey returns only agents granting "ghapp".
	listed, err := acli.Agents(ctx, "", "", 0)
	if err != nil || len(listed) != 1 || listed[0].ID != aid {
		t.Fatalf("akey GET /agents got %+v err=%v, want only %s", listed, err, aid)
	}

	// POST /agents/{aid}/records forces via="app:ghapp" even when caller passes via="spoofed".
	sid, _, err := acli.PostAgent(ctx, aid, "spoofed", "pr comment", nil)
	if err != nil {
		t.Fatalf("akey PostAgent failed: %v", err)
	}
	rig.waitAgentIdle(t, aid)
	chain, _ := rig.h.Sessions.Load(sid)
	if !slices.ContainsFunc(chain, func(r statefile.Record) bool {
		return r.Kind == statefile.Message && string(r.Body) == "pr comment" && r.Via == "app:ghapp"
	}) {
		t.Fatal("akey post did not force via=app:ghapp")
	}

	// POST to otherID (which lacks "ghapp") fails 403; /secrets fails 403.
	if _, _, err := acli.PostAgent(ctx, otherID, "app:ghapp", "nope", nil); err == nil {
		t.Fatal("akey post to agent without ghapp must fail 403")
	}
	if _, err := acli.Secrets(ctx); err == nil {
		t.Fatal("akey must fail 403 on /secrets")
	}
}

// Covers: orchestrate (subset 403 on workers & apps, via="agent:<caller>", silent default vs notify_on_stop=true across mid-run rollover).
func TestConformanceOrchestrate(t *testing.T) {
	rig := newTestRig(t)
	startFakeWorker(t, rig.pool, "w1")
	startFakeWorker(t, rig.pool, "w2")
	ctx := context.Background()

	bossID, bossS1, err := rig.cli.CreateAgent(ctx, harness.AgentCreate{
		Name:   "boss",
		Config: defaultAgentCfg("w1"),
		Apps:   []string{"orchestrate", "message"},
	}, "web")
	if err != nil {
		t.Fatal(err)
	}
	bossCli := &connector.Client{URL: rig.srv.URL, Pinned: true, Key: auth.SessionToken(rig.priv, rig.hkey, bossS1), Local: rig.srv.URL}

	// 1. Escalation checks: workers=[w1,w2] -> 403; apps=["schedule"] -> 403.
	_, err = bossCli.CallApp(ctx, "orchestrate", "POST", "/spawn", "application/json",
		strings.NewReader(`{"name":"badw","config":{"workers":[{"name":"w1"},{"name":"w2"}]}}`))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 on worker escalation, got %v", err)
	}
	_, err = bossCli.CallApp(ctx, "orchestrate", "POST", "/spawn", "application/json",
		strings.NewReader(`{"name":"bada","apps":["schedule"]}`))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 on app escalation, got %v", err)
	}

	// 2. Valid silent spawn: gets via="agent:<bossID>", finishes silently without waking boss.
	raw, err := bossCli.CallApp(ctx, "orchestrate", "POST", "/spawn", "application/json",
		strings.NewReader(`{"name":"silent-sub","apps":["message"],"text":"do quiet work"}`))
	if err != nil {
		t.Fatalf("spawn silent-sub: %v", err)
	}
	var sp1 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &sp1)
	sub1 := rig.waitAgentIdle(t, sp1.ID)
	if sub1.Via != "agent:"+bossID {
		t.Fatalf("child via=%q, want agent:%s", sub1.Via, bossID)
	}
	time.Sleep(100 * time.Millisecond)
	bossChain, _ := rig.h.Sessions.Load(bossS1)
	if slices.ContainsFunc(bossChain, func(r statefile.Record) bool { return r.Kind == statefile.Message }) {
		t.Fatal("silent subagent unexpectedly sent a message to boss on close")
	}

	verifyOrchestrateNotifyAfterRollover(t, rig, bossCli, bossID, bossS1)
}

func verifyOrchestrateNotifyAfterRollover(t *testing.T, rig *testRig, bossCli *connector.Client, bossID, bossS1 string) {
	t.Helper()
	// Subagent C2 hits mid-run rollover on step 1 -> must NOT fire notify_on_stop mid-rollover,
	// then closes on step 2 -> fires exactly ONE notice with via="app:orchestrate" to boss!
	var c2Steps atomic.Int32
	var bossMsgsDuringRollover atomic.Int32
	rig.setStep(func(_ context.Context, model, _ string, recs []statefile.Record, _ *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
		// Boss wake-up step vs C2 steps: check if any record is via="app:orchestrate".
		if slices.ContainsFunc(recs, func(r statefile.Record) bool { return r.Via == "app:orchestrate" }) {
			emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("ack")})
			return statefile.Header{Model: model, Stop: "end_turn", Usage: &statefile.Usage{Input: 100, Output: 10}}, nil
		}
		n := c2Steps.Add(1)
		if n == 1 {
			time.Sleep(40 * time.Millisecond) // outlive /spawn HTTP request context
			callBody, _ := json.Marshal(map[string]any{
				"name":      "run",
				"arguments": map[string]any{"worker": "w1", "code": "echo step1"},
			})
			emit(statefile.Record{Header: statefile.Header{Kind: statefile.Call, Type: "application/json"}, Body: callBody})
			return statefile.Header{Model: model, Stop: "tool_use", Usage: &statefile.Usage{Input: 60000, Output: 500}}, nil
		}
		// At start of C2's S2 step, boss must still have 0 messages!
		bc, _ := rig.h.Sessions.Load(bossS1)
		for _, r := range bc {
			if r.Kind == statefile.Message {
				bossMsgsDuringRollover.Add(1)
			}
		}
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("c2 finished")})
		return statefile.Header{Model: model, Stop: "end_turn", Usage: &statefile.Usage{Input: 1000, Output: 50}}, nil
	})

	spawnBody := `{"name":"watched-sub","config":{"context_limit":50000},"apps":["message"],"text":"long job","notify_on_stop":true}`
	raw, err := bossCli.CallApp(context.Background(), "orchestrate", "POST", "/spawn", "application/json", bytes.NewBufferString(spawnBody))
	if err != nil {
		t.Fatalf("spawn watched-sub: %v", err)
	}
	var sp2 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &sp2)
	rig.waitAgentIdle(t, sp2.ID)
	if bossMsgsDuringRollover.Load() != 0 {
		t.Fatalf("notify_on_stop fired prematurely during mid-run rollover (%d msgs)", bossMsgsDuringRollover.Load())
	}

	// Wait for boss to receive the single terminal stop notice and finish stepping.
	deadline := time.Now().Add(5 * time.Second)
	notices := 0
	for time.Now().Before(deadline) {
		bc, _ := rig.h.Sessions.Load(bossS1)
		notices = 0
		for _, r := range bc {
			if r.Kind == statefile.Message && r.Via == "app:orchestrate" && strings.Contains(string(r.Body), sp2.ID) {
				notices++
			}
		}
		if notices > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	rig.waitAgentIdle(t, bossID)
	if notices != 1 {
		t.Fatalf("expected 1 app:orchestrate notice on boss after C2 terminal close, got %d", notices)
	}
}
