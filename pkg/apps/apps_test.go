package apps

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

func TestHelperDaemonProcess(t *testing.T) {
	if !slices.Contains(os.Args, "daemon-mode") {
		return
	}
	port := os.Getenv("PORT")
	proxySecret := os.Getenv("MAINPLANE_PROXY_SECRET")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Mainplane-Proxy-Secret") != proxySecret {
			http.Error(w, "bad proxy secret", http.StatusForbidden)
			return
		}
		reply := map[string]string{
			"path":      r.URL.Path,
			"agent":     r.Header.Get("X-Mainplane-Agent"),
			"session":   r.Header.Get("X-Mainplane-Session"),
			"data_dir":  os.Getenv("DATA_DIR"),
			"app_key":   os.Getenv("MAINPLANE_APP_KEY"),
			"secret_k":  os.Getenv("SECRET_K"),
			"secret_k2": os.Getenv("SECRET_K2"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, mux)
	os.Exit(0)
}

type fakeHost struct {
	mu     sync.Mutex
	agents map[string]AgentInfo
	posts  []postedMsg
}

type postedMsg struct {
	id, via, text string
}

func newFakeHost() *fakeHost {
	return &fakeHost{agents: map[string]AgentInfo{}}
}

func (f *fakeHost) setAgent(a AgentInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents[a.ID] = a
}

func (f *fakeHost) LiveAgent(sid string) (AgentInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agents {
		if a.CurrentSession == sid {
			return a, true
		}
	}
	return AgentInfo{}, false
}

func (f *fakeHost) AgentHasApp(id, app string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	return ok && slices.Contains(a.Apps, app)
}

func (f *fakeHost) GetAgent(id string) (AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return AgentInfo{}, fs.ErrNotExist
	}
	return a, nil
}

func (f *fakeHost) ListAgents() ([]AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AgentInfo
	for _, a := range f.agents {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeHost) CreateAgent(_ context.Context, _ AgentCreate, _ string) (string, string, error) {
	return "", "", errors.New("not used")
}

func (f *fakeHost) PostText(_ context.Context, id, via, text string) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return "", 0, fs.ErrNotExist
	}
	f.posts = append(f.posts, postedMsg{id: id, via: via, text: text})
	return a.CurrentSession, len(f.posts), nil
}

func (f *fakeHost) SetAgentName(_, _, _ string) error                { return nil }
func (f *fakeHost) SetAgentConfig(_, _ string, _ AgentConfig) error  { return nil }
func (f *fakeHost) SetAgentApps(_, _ string, _ []string) error       { return nil }
func (f *fakeHost) StopAgent(_ context.Context, _, _ string) error   { return nil }
func (f *fakeHost) DeleteAgent(_ context.Context, _, _ string) error { return nil }
func (f *fakeHost) SessionRecords(_ context.Context, _ string, _ int, _ time.Duration) ([]statefile.Record, error) {
	return nil, nil
}

type appsHarness struct {
	dir     string
	host    *fakeHost
	mgr     *Manager
	srv     *httptest.Server
	priv    ed25519.PrivateKey
	hkey    string
	fullKey string
}

func setupAppsHarness(t *testing.T, dir string, host *fakeHost) (*appsHarness, context.CancelFunc) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hkey := "testhkey"
	store := auth.Store{Path: filepath.Join(dir, "auth.json")}
	fullTok, err := store.Issue(auth.Key, "admin-"+rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	var srvURL string
	mgr, err := New(Config{
		Dir:         dir,
		Host:        host,
		LoopbackURL: func() string { return srvURL },
		AppToken:    func(app string) string { return auth.AppToken(priv, hkey, app) },
	})
	if err != nil {
		t.Fatal(err)
	}
	routes := http.NewServeMux()
	mgr.Register(routes)
	mux := http.NewServeMux()
	mgr.RegisterWebhooks(mux)
	mux.Handle("/", store.Gate(priv, hkey, &auth.Limit{}, routes))
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})
	return &appsHarness{
		dir:     dir,
		host:    host,
		mgr:     mgr,
		srv:     srv,
		priv:    priv,
		hkey:    hkey,
		fullKey: fullTok,
	}, cancel
}

func writeDiskApp(t *testing.T, base, name, skill string, man *Manifest) {
	t.Helper()
	d := filepath.Join(base, "apps", name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if skill != "" {
		if err := os.WriteFile(filepath.Join(d, "skill.md"), []byte(skill), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if man != nil {
		b, err := json.Marshal(man)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "app.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func doReq(t *testing.T, method, url, bearer, body string, extra map[string]string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func TestSecretIsolationAndDaemonAutoStart(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runCmd := []string{exe, "-test.run=^TestHelperDaemonProcess$", "--", "daemon-mode"}
	// App A declares SECRET_K and entitlements=["post"]; App B declares neither.
	writeDiskApp(t, dir, "appa", "App A summary\nDetails A\n", &Manifest{
		Run:          runCmd,
		Secrets:      []string{"SECRET_K"},
		Entitlements: []string{"post"},
	})
	writeDiskApp(t, dir, "appb", "App B summary\nDetails B\n", &Manifest{
		Run: runCmd,
	})

	host := newFakeHost()
	host.setAgent(AgentInfo{ID: "a1111111", CurrentSession: "s1111111", Apps: []string{"appa", "appb"}})
	ah, _ := setupAppsHarness(t, dir, host)
	skey := auth.SessionToken(ah.priv, ah.hkey, "s1111111")

	// Before SECRET_K is set, appa is missing_secrets and fails fast naming SECRET_K.
	code, body := doReq(t, "GET", ah.srv.URL+"/apps/appa/ping", skey, "", nil)
	if code == http.StatusOK || !bytes.Contains(body, []byte("SECRET_K")) {
		t.Fatalf("expected missing SECRET_K failure, got %d %s", code, body)
	}

	// Set SECRET_K and SECRET_K2; GET /secrets lists names only without values.
	code, _ = doReq(t, "PUT", ah.srv.URL+"/secrets/SECRET_K", ah.fullKey, "super-secret-val", nil)
	if code != http.StatusNoContent {
		t.Fatalf("PUT secret: %d", code)
	}
	code, _ = doReq(t, "PUT", ah.srv.URL+"/secrets/SECRET_K2", ah.fullKey, "other-secret-val", nil)
	if code != http.StatusNoContent {
		t.Fatalf("PUT secret 2: %d", code)
	}
	code, body = doReq(t, "GET", ah.srv.URL+"/secrets", ah.fullKey, "", nil)
	if code != http.StatusOK || bytes.Contains(body, []byte("super-secret-val")) || !bytes.Contains(body, []byte("SECRET_K")) {
		t.Fatalf("GET /secrets leaked or missed name: %d %s", code, body)
	}
	if fi, err := os.Stat(filepath.Join(dir, ".secrets")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".secrets perm got %v, err %v", fi, err)
	}

	verifyDaemonIsolation(t, ah, skey)
}

func verifyDaemonIsolation(t *testing.T, ah *appsHarness, skey string) {
	t.Helper()
	// appa auto-started and receives SECRET_K + MAINPLANE_APP_KEY, NOT SECRET_K2.
	code, body := doReq(t, "GET", ah.srv.URL+"/apps/appa/hello", skey, "", map[string]string{
		"X-Mainplane-Agent": "spoofed",
	})
	if code != http.StatusOK {
		t.Fatalf("appa call: %d %s", code, body)
	}
	var gotA map[string]string
	if err := json.Unmarshal(body, &gotA); err != nil {
		t.Fatal(err)
	}
	if gotA["secret_k"] != "super-secret-val" || gotA["secret_k2"] != "" {
		t.Fatalf("appa secret isolation failed: %+v", gotA)
	}
	if !strings.HasPrefix(gotA["app_key"], "mp_akey_") || gotA["agent"] != "a1111111" || gotA["session"] != "s1111111" {
		t.Fatalf("appa headers/app_key wrong: %+v", gotA)
	}

	// appb has secrets=[] and entitlements=[]: receives NO SECRET_K and NO MAINPLANE_APP_KEY.
	code, body = doReq(t, "GET", ah.srv.URL+"/apps/appb/hello", skey, "", nil)
	if code != http.StatusOK {
		t.Fatalf("appb call: %d %s", code, body)
	}
	var gotB map[string]string
	if err := json.Unmarshal(body, &gotB); err != nil {
		t.Fatal(err)
	}
	if gotB["secret_k"] != "" || gotB["app_key"] != "" {
		t.Fatalf("appb leaked secret or app_key: %+v", gotB)
	}

	// Stop and restart appb via admin routes.
	if c, _ := doReq(t, "POST", ah.srv.URL+"/apps/appb/stop", ah.fullKey, "", nil); c != http.StatusNoContent {
		t.Fatalf("stop appb: %d", c)
	}
	if c, _ := doReq(t, "GET", ah.srv.URL+"/apps/appb/hello", skey, "", nil); c == http.StatusOK {
		t.Fatal("stopped appb still answered 200")
	}
	if c, _ := doReq(t, "POST", ah.srv.URL+"/apps/appb/start", ah.fullKey, "", nil); c != http.StatusNoContent {
		t.Fatalf("start appb: %d", c)
	}
	if c, _ := doReq(t, "GET", ah.srv.URL+"/apps/appb/hello", skey, "", nil); c != http.StatusOK {
		t.Fatalf("restarted appb failed: %d", c)
	}
}

func TestURLProxyAndWebhooks(t *testing.T) {
	var seenHdr http.Header
	var seenPath string
	var mu sync.Mutex
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenHdr = r.Header.Clone()
		seenPath = r.URL.Path
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()

	dir := t.TempDir()
	writeDiskApp(t, dir, "extapi", "External API\n", &Manifest{
		URL:      up.URL,
		Secrets:  []string{"EXT_KEY"},
		Headers:  map[string]string{"X-Api-Key": "token-$EXT_KEY"},
		Webhooks: true,
	})
	writeDiskApp(t, dir, "nohook", "No webhook app\n", &Manifest{
		URL:      up.URL,
		Webhooks: false,
	})

	host := newFakeHost()
	host.setAgent(AgentInfo{ID: "a2222222", CurrentSession: "s2222222", Apps: []string{"extapi"}})
	ah, _ := setupAppsHarness(t, dir, host)
	skey := auth.SessionToken(ah.priv, ah.hkey, "s2222222")

	doReq(t, "PUT", ah.srv.URL+"/secrets/EXT_KEY", ah.fullKey, "xyz987", nil)

	code, _ := doReq(t, "POST", ah.srv.URL+"/apps/extapi/v1/search", skey, `{"q":"hi"}`, map[string]string{
		"X-Mainplane-Agent": "forged",
	})
	if code != http.StatusOK {
		t.Fatalf("proxy status %d", code)
	}
	mu.Lock()
	if seenPath != "/v1/search" || seenHdr.Get("X-Api-Key") != "token-xyz987" || seenHdr.Get("Authorization") != "" || seenHdr.Get("X-Mainplane-Agent") != "a2222222" {
		t.Fatalf("proxy headers/path wrong: path=%s hdr=%v", seenPath, seenHdr)
	}
	mu.Unlock()

	// Unauthenticated webhook succeeds on webhooks=true with NO X-Mainplane-Agent even if spoofed.
	code, _ = doReq(t, "POST", ah.srv.URL+"/webhooks/extapi/github", "", `{"ev":1}`, map[string]string{
		"X-Mainplane-Agent": "forged-webhook",
	})
	if code != http.StatusOK {
		t.Fatalf("webhook status %d", code)
	}
	mu.Lock()
	if seenPath != "/webhooks/github" || seenHdr.Get("X-Mainplane-Agent") != "" {
		t.Fatalf("webhook path/headers wrong: path=%s hdr=%v", seenPath, seenHdr)
	}
	mu.Unlock()

	// Webhook on webhooks=false returns 404.
	code, _ = doReq(t, "POST", ah.srv.URL+"/webhooks/nohook/events", "", `{}`, nil)
	if code != http.StatusNotFound {
		t.Fatalf("disabled webhook got %d, want 404", code)
	}
}

func TestScheduleAndMessageBuiltins(t *testing.T) {
	dir := t.TempDir()
	host := newFakeHost()
	host.setAgent(AgentInfo{ID: "a1111111", CurrentSession: "s1111111", Apps: []string{"schedule", "message"}})
	host.setAgent(AgentInfo{ID: "b2222222", CurrentSession: "s2222222", Apps: []string{}})

	ah, cancel := setupAppsHarness(t, dir, host)
	skeyA := auth.SessionToken(ah.priv, ah.hkey, "s1111111")

	// 1. message: one-way permission to b2222222 (which lacks "message"), GET is 405.
	if c, _ := doReq(t, "GET", ah.srv.URL+"/apps/message", skeyA, "", nil); c != http.StatusMethodNotAllowed {
		t.Fatalf("GET /apps/message got %d, want 405", c)
	}
	if c, _ := doReq(t, "POST", ah.srv.URL+"/apps/message", skeyA, `{"to":"b2222222","text":"ping from A"}`, nil); c != http.StatusOK {
		t.Fatalf("POST /apps/message got %d", c)
	}
	host.mu.Lock()
	if len(host.posts) != 1 || host.posts[0].id != "b2222222" || host.posts[0].via != "agent:a1111111" || host.posts[0].text != "ping from A" {
		t.Fatalf("unexpected message posts: %+v", host.posts)
	}
	host.mu.Unlock()

	// 2. schedule: create a cron job (persists across restart) and a 100ms one-shot timer.
	if c, b := doReq(t, "POST", ah.srv.URL+"/apps/schedule", skeyA, `{"text":"cron-check","cron":"*/15 * * * *"}`, nil); c != http.StatusOK {
		t.Fatalf("POST cron: %d %s", c, b)
	}
	if c, b := doReq(t, "POST", ah.srv.URL+"/apps/schedule", skeyA, `{"text":"wake-now","in":"100ms"}`, nil); c != http.StatusOK {
		t.Fatalf("POST one-shot: %d %s", c, b)
	}
	waitForSchedulePost(t, host, "wake-now")

	// Restart manager on same dir -> cron job survives in jobs.json.
	cancel()
	ah.srv.Close()
	ah2, _ := setupAppsHarness(t, dir, host)
	skeyA2 := auth.SessionToken(ah2.priv, ah2.hkey, "s1111111")
	code, listBody := doReq(t, "GET", ah2.srv.URL+"/apps/schedule", skeyA2, "", nil)
	if code != http.StatusOK || !bytes.Contains(listBody, []byte("cron-check")) {
		t.Fatalf("cron job did not survive restart: %d %s", code, listBody)
	}

	// Remove "schedule" from agent A and fire a short timer -> dropped without posting.
	doReq(t, "POST", ah2.srv.URL+"/apps/schedule", skeyA2, `{"text":"dropped-timer","in":"80ms"}`, nil)
	host.setAgent(AgentInfo{ID: "a1111111", CurrentSession: "s1111111", Apps: []string{"message"}})
	time.Sleep(350 * time.Millisecond)
	host.mu.Lock()
	for _, p := range host.posts {
		if p.text == "dropped-timer" {
			t.Fatalf("timer fired after schedule app was removed: %+v", p)
		}
	}
	host.mu.Unlock()
}

func waitForSchedulePost(t *testing.T, host *fakeHost, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		for _, p := range host.posts {
			if p.text == want && p.via == "app:schedule" && p.id == "a1111111" {
				host.mu.Unlock()
				return
			}
		}
		host.mu.Unlock()
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for schedule post %q", want)
}
