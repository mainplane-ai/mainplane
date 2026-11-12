package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// turn is what the test's model answers a step with: a run of code on w1,
// or with no code the text "ok" and the turn's end. input is the prompt's
// size as the step reports it, ttl how long its prompt cache lasts.
type turn struct {
	code       string
	input, ttl int
}

// rig is a harness whose model is the test's and whose one worker, w1,
// keeps its files under dir.
type rig struct {
	t   *testing.T
	h   *Harness
	dir string

	mu      sync.Mutex
	steps   int
	step    func(body string) turn // the model, given the request
	events  []string               // what w1 was asked, in order
	stopped []string               // each call of h.Stopped
	started chan struct{}          // a run of "wait" began
	release chan struct{}          // closed to let it end
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, dir: t.TempDir(), started: make(chan struct{}, 1), release: make(chan struct{})}
	r.step = func(string) turn { return turn{input: 10} }
	p := provider.Anthropic("", "k")
	p.Run = func(ctx context.Context, body []byte, cache *provider.Cache, emit func(statefile.Record)) (statefile.Header, error) {
		r.mu.Lock()
		r.steps++
		step := r.step
		r.mu.Unlock()
		tn := step(string(body))
		*cache = provider.Cache{TTL: tn.ttl}
		hdr := statefile.Header{Stop: "end_turn", Usage: &statefile.Usage{Input: tn.input}}
		if tn.code == "" {
			emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte("ok")})
			return hdr, nil
		}
		hdr.Stop = "tool_use"
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Call, Type: "application/json"}, Body: fmt.Appendf(nil, `{"name":"run","arguments":{"worker":"w1","interpreter":"bash","code":%q}}`, tn.code)})
		return hdr, nil
	}
	r.h = &Harness{
		Sessions:  statefile.Sessions{Dir: filepath.Join(r.dir, "sessions")},
		Providers: map[string]provider.Provider{"fake": p},
		Workers:   NewPool(func(string) worker.Desired { return worker.Desired{} }, func(string) string { return "" }),
		Prompt:    func() ([]string, error) { return nil, nil },
		Agents:    filepath.Join(r.dir, "agents"),
		Skill:     func(app string) (string, bool) { return "what " + app + " does", app != "nope" },
		Stopped: func(a Agent, st statefile.Status) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.stopped = append(r.stopped, a.live()+" "+string(st))
		},
	}
	if err := os.MkdirAll(r.h.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.h.LoadAgents(); err != nil {
		t.Fatal(err)
	}
	near, far := net.Pipe()
	t.Cleanup(func() { _ = near.Close() })
	go r.serve(far)
	rem, err := Connect(near, func() (string, error) { return "w1", nil })
	if err != nil {
		t.Fatal(err)
	}
	r.h.Workers.Add(rem)
	return r
}

// serve is w1: a worker that writes the files it is sent under dir, and
// answers a run with "ran". A run of "wait" says it began and waits for
// release.
func (r *rig) serve(conn net.Conn) {
	var wmu sync.Mutex
	send := func(f worker.Frame) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = worker.Encode(conn, f)
	}
	send(worker.Frame{Header: worker.Header{Kind: worker.Hello, OS: "linux", Interps: []string{"bash"}, Scratch: "/scratch", Version: version.V}})
	br := bufio.NewReader(conn)
	for {
		f, err := worker.Decode(br)
		if err != nil {
			return
		}
		go func() {
			reply := worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Done}}
			event := f.Kind + " " + f.Session + f.Path
			switch f.Kind {
			case worker.Run:
				if string(f.Body) == "wait" {
					r.started <- struct{}{}
					<-r.release
				}
				send(worker.Frame{Header: worker.Header{ID: f.ID, Kind: worker.Output}, Body: []byte("ran\n")})
				reply.Kind = worker.Result
			case worker.Write:
				path := filepath.Join(r.dir, f.Path)
				_ = os.MkdirAll(filepath.Dir(path), 0o755)
				_ = os.WriteFile(path, f.Body, 0o644)
			case worker.Read:
				reply.Kind, reply.Body = worker.Error, []byte("no such file")
			case worker.Drives:
				return
			}
			r.mu.Lock()
			r.events = append(r.events, event)
			r.mu.Unlock()
			send(reply)
		}()
	}
}

func (r *rig) conf(limit, cold int, workers ...string) *AgentConfig {
	c := &AgentConfig{Conf: statefile.Conf{Model: "fake/m1", ContextLimit: limit, Workers: []statefile.Worker{}}, ColdLimit: cold}
	for _, w := range workers {
		c.Workers = append(c.Workers, statefile.Worker{Name: w})
	}
	return c
}

func (r *rig) create(c AgentCreate, via string) Agent {
	r.t.Helper()
	a, err := r.h.CreateAgent(context.Background(), c, via)
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

// post is a message to the agent, and the session it landed on once that
// session's run has ended.
func (r *rig) post(id, text string) string {
	r.t.Helper()
	s, _, err := r.h.PostAgent(context.Background(), id, "test", "", []Part{{Type: "text/plain", Body: []byte(text)}})
	if err != nil {
		r.t.Fatal(err)
	}
	r.settled(id)
	return s
}

// settled waits for the agent's live session to end its run closed.
func (r *rig) settled(id string) {
	r.t.Helper()
	r.until(func() bool {
		i, err := r.h.AgentInfo(id)
		return err == nil && i.Status == statefile.StatusClosed
	})
}

func (r *rig) until(ok func() bool) {
	r.t.Helper()
	for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			r.t.Fatal("timed out waiting")
		}
	}
}

func (r *rig) chain(session string) []statefile.Record {
	r.t.Helper()
	chain, err := r.h.Sessions.Load(session)
	if err != nil {
		r.t.Fatal(err)
	}
	return chain
}

// bodies is the body of each record of the kind, and for a system record of
// the source, in the session.
func (r *rig) bodies(session string, kind statefile.Kind, source string) []string {
	var out []string
	for _, rec := range r.chain(session) {
		if rec.Kind == kind && rec.Source == source {
			out = append(out, string(rec.Body))
		}
	}
	return out
}

func (r *rig) sessions(id string) []string {
	r.t.Helper()
	a, err := r.h.Agent(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return a.Sessions
}

// create-1trip, create-2trip and via-start.
func TestCreateAgent(t *testing.T) {
	r := newRig(t)
	a := r.create(AgentCreate{Name: "one", Config: r.conf(1000, 0), Apps: []string{"schedule", "message", "message"}}, "web")
	var onDisk Agent
	b, err := os.ReadFile(filepath.Join(r.h.Agents, a.ID+".json"))
	if err != nil || json.Unmarshal(b, &onDisk) != nil {
		t.Fatalf("agent file: %v", err)
	}
	if len(onDisk.Sessions) != 1 || onDisk.Via != "web" || !slices.Equal(onDisk.Apps, []string{"message", "schedule"}) {
		t.Fatalf("agent file: %s", b)
	}
	info, err := r.h.AgentInfo(a.ID)
	if err != nil || info.Status != statefile.StatusClosed || info.CurrentSession != onDisk.Sessions[0] || r.steps != 0 {
		t.Fatalf("info: %+v %v, %d model calls", info, err, r.steps)
	}
	chain := r.chain(a.Sessions[0])
	if chain[0].Kind != statefile.Start || chain[0].Via != "agent:"+a.ID {
		t.Fatalf("record 1: %+v", chain[0].Header)
	}
	if notes := r.bodies(a.Sessions[0], statefile.System, "apps"); len(notes) != 1 || !strings.Contains(notes[0], "message: what message does\nschedule: what schedule does\n") || !strings.Contains(notes[0], a.ID) {
		t.Fatalf("apps note: %q", notes)
	}
	if _, err := r.h.CreateAgent(context.Background(), AgentCreate{Config: r.conf(1000, 0), Apps: []string{"nope"}}, "web"); err == nil {
		t.Fatal("an agent was made with an app the harness does not have")
	}
	if _, err := r.h.CreateAgent(context.Background(), AgentCreate{}, "web"); err == nil {
		t.Fatal("an agent was made with no config and no session")
	}

	// a plain session, then an agent that adopts it
	s1, err := r.h.Create(context.Background(), Create{Model: "fake/m1", ContextLimit: 777, Workers: []statefile.Worker{{Name: "w1"}}}, "tui")
	if err != nil {
		t.Fatal(err)
	}
	chain = r.chain(s1)
	if chain[0].Via != "tui" {
		t.Fatalf("record 1: %+v", chain[0].Header)
	}
	built, _ := statefile.Build(chain)
	if slices.ContainsFunc(built, func(rec statefile.Record) bool { return rec.Kind == statefile.Start }) {
		t.Fatal("Build sends the start record to the model")
	}
	if _, ok := r.h.Holder(s1); ok {
		t.Fatal("a plain session has an agent")
	}
	two := r.create(AgentCreate{Session: s1, Apps: []string{"message"}}, "desktop")
	if !slices.Equal(two.Sessions, []string{s1}) || two.Config.Model != "fake/m1" || two.Config.ContextLimit != 777 || len(two.Config.Workers) != 1 || two.Config.Tools != "default" {
		t.Fatalf("adopted: %+v", two)
	}
	if notes := r.bodies(s1, statefile.System, "apps"); len(notes) != 1 || !strings.Contains(notes[0], two.ID) {
		t.Fatalf("apps note: %q", notes)
	}
	if h, ok := r.h.Holder(s1); !ok || h.ID != two.ID {
		t.Fatalf("holder: %+v", h)
	}
	if _, err := r.h.CreateAgent(context.Background(), AgentCreate{Session: s1}, "desktop"); err == nil || !strings.Contains(err.Error(), two.ID) {
		t.Fatalf("a second agent took the session: %v", err)
	}
	if r.steps != 0 {
		t.Fatalf("%d model calls", r.steps)
	}

	// memory is the files: a harness that starts over has the same agents
	r.h.agents, r.h.live = nil, nil
	if err := r.h.LoadAgents(); err != nil {
		t.Fatal(err)
	}
	if h, ok := r.h.Holder(s1); !ok || h.ID != two.ID || len(r.h.ListAgents(func(Agent) bool { return true })) != 2 {
		t.Fatalf("after a reload: %+v", h)
	}
}

// lazy-config, warm-fastpath and warm-vs-cold: what an idle agent's next
// post lands on.
func TestWarmFastPath(t *testing.T) {
	r := newRig(t)
	r.step = func(string) turn { return turn{input: 140_000, ttl: 300} }
	a := r.create(AgentCreate{Config: r.conf(250_000, 100_000, "w1"), Apps: []string{"message"}}, "web")
	s1 := a.Sessions[0]
	if got := r.post(a.ID, "one"); got != s1 {
		t.Fatalf("first post landed on %s", got)
	}

	// warm-fastpath: workers and apps change the live session in place
	c := r.conf(250_000, 100_000, "w1", "w2")
	if err := r.h.SetAgentConfig(a.ID, "web", *c); err != nil {
		t.Fatal(err)
	}
	if err := r.h.SetAgentApps(a.ID, "web", []string{"message", "schedule"}); err != nil {
		t.Fatal(err)
	}
	conf, _ := config(r.chain(s1))
	if len(conf.Workers) != 2 || conf.Workers[1].Name != "w2" {
		t.Fatalf("workers of the live session: %+v", conf.Workers)
	}
	if notes := r.bodies(s1, statefile.System, "apps"); len(notes) != 2 || !strings.Contains(notes[1], "schedule: ") {
		t.Fatalf("apps notes: %q", notes)
	}
	if err := r.h.SetAgentApps(a.ID, "web", []string{"nope"}); err == nil {
		t.Fatal("an agent took an app the harness does not have")
	}
}

func TestIdleRollover(t *testing.T) {
	r := newRig(t)
	ttl := 300
	r.step = func(string) turn { return turn{input: 140_000, ttl: ttl} }
	a := r.create(AgentCreate{Config: r.conf(250_000, 100_000, "w1"), Apps: []string{"message"}}, "web")
	s1 := a.Sessions[0]
	if got := r.post(a.ID, "one"); got != s1 {
		t.Fatalf("first post landed on %s", got)
	}

	// warm-vs-cold: 140k of 250k, cold limit 100k. Warm, the post stays.
	ttl = 1
	if got := r.post(a.ID, "two"); got != s1 || len(r.sessions(a.ID)) != 1 {
		t.Fatalf("a post on a warm cache landed on %s: %v", got, r.sessions(a.ID))
	}
	// Cold, it goes to a new session, which is told of the old one.
	time.Sleep(1100 * time.Millisecond)
	s2 := r.post(a.ID, "three")
	if got := r.sessions(a.ID); s2 == s1 || !slices.Equal(got, []string{s1, s2}) {
		t.Fatalf("a post on a cold cache landed on %s: %v", s2, got)
	}
	if got := r.bodies(s1, statefile.System, "continue"); !slices.Equal(got, []string{s2}) {
		t.Fatalf("continue of s1: %q", got)
	}
	if statefile.Derive(r.chain(s1)) != statefile.StatusClosed {
		t.Fatal("the session left behind is not closed")
	}
	if got := r.bodies(s2, statefile.System, "continue"); len(got) != 1 || !strings.Contains(got[0], "/logs/"+s1+".log") || !strings.Contains(got[0], "cold limit") {
		t.Fatalf("continue of s2: %q", got)
	}
	if got := r.bodies(s2, statefile.System, "apps"); len(got) != 1 || !strings.Contains(got[0], "message: ") {
		t.Fatalf("apps of s2: %q", got)
	}
	if got := r.bodies(s2, statefile.Message, ""); !slices.Equal(got, []string{"three"}) {
		t.Fatalf("messages of s2: %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(r.dir, "scratch", "logs", s1+".log")); err != nil || !strings.Contains(string(b), "two") {
		t.Fatalf("log of s1 on the worker: %v", err)
	}
	if conf, _ := config(r.chain(s2)); len(conf.Workers) != 1 || r.chain(s2)[0].Via != "agent:"+a.ID {
		t.Fatalf("s2: %+v", conf)
	}

	// lazy-config: an edit makes no session; the next post does
	ttl = 300
	c := r.conf(250_000, 100_000, "w1")
	c.Model = "fake/m2"
	for range 3 {
		if err := r.h.SetAgentConfig(a.ID, "web", *c); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.sessions(a.ID); len(got) != 2 {
		t.Fatalf("an edit made a session: %v", got)
	}
	s3 := r.post(a.ID, "four")
	if conf, _ := config(r.chain(s3)); s3 == s2 || conf.Model != "fake/m2" || len(r.sessions(a.ID)) != 3 {
		t.Fatalf("after a model change the post landed on %s: %+v", s3, conf)
	}
	if got := r.bodies(s3, statefile.System, "continue"); len(got) != 1 || !strings.Contains(got[0], "config changed") || !strings.Contains(got[0], s1+", "+s2) {
		t.Fatalf("continue of s3: %q", got)
	}
}

// At context_limit a post rolls over even on a warm cache, and a post the
// live session already holds never moves.
func TestLimitRollover(t *testing.T) {
	r := newRig(t)
	r.step = func(string) turn { return turn{input: 140_000, ttl: 300} }
	a := r.create(AgentCreate{Config: r.conf(140_000, 100_000, "w1")}, "web")
	s1 := a.Sessions[0]
	if got := r.post(a.ID, "one"); got != s1 {
		t.Fatalf("first post landed on %s", got)
	}
	if s2 := r.post(a.ID, "two"); s2 == s1 || len(r.sessions(a.ID)) != 2 {
		t.Fatalf("at the limit the post landed on %s", s2)
	}
	// a post the live session already holds never moves
	n := len(r.sessions(a.ID))
	for range 2 {
		if _, _, err := r.h.PostAgent(context.Background(), a.ID, "test", "key-1", []Part{{Type: "text/plain", Body: []byte("six")}}); err != nil {
			t.Fatal(err)
		}
		r.settled(a.ID)
	}
	if got := r.sessions(a.ID); len(got) != n+1 || len(r.bodies(got[n], statefile.Message, "")) != 1 {
		t.Fatalf("a repeated post: %v", got)
	}
}

// An agent's usage is the sum over its sessions, the earlier ones from the
// index and not from their files; a deleted session's share goes with it.
func TestAgentUsage(t *testing.T) {
	r := newRig(t)
	r.step = func(string) turn { return turn{input: 140_000} }
	a := r.create(AgentCreate{Config: r.conf(140_000, 0, "w1")}, "web")
	s1 := r.post(a.ID, "one")
	s2 := r.post(a.ID, "two")
	s3 := r.post(a.ID, "three")
	if s1 == s2 || s2 == s3 {
		t.Fatalf("no rollover: %s %s %s", s1, s2, s3)
	}
	usage := func() int {
		t.Helper()
		i, err := r.h.AgentInfo(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		return i.Usage.Input
	}
	if got := usage(); got != 420_000 {
		t.Fatalf("usage over three sessions: %d", got)
	}
	if i, err := r.h.Info(s3); err != nil || i.Usage.Input != 140_000 {
		t.Fatalf("the live session's own usage: %v %v", i.Usage, err)
	}
	// with its file out of the way an earlier session still counts: nothing read it
	if err := os.Rename(r.h.Sessions.Path(s1), r.h.Sessions.Path(s1)+".away"); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != 420_000 {
		t.Fatalf("usage with an earlier session's file away: %d", got)
	}
	if list := r.h.ListAgents(func(Agent) bool { return true }); len(list) != 1 || list[0].Usage.Input != 420_000 {
		t.Fatalf("list: %+v", list)
	}
	if err := os.Rename(r.h.Sessions.Path(s1)+".away", r.h.Sessions.Path(s1)); err != nil {
		t.Fatal(err)
	}
	// a write to an earlier session is read again
	if _, err := r.h.Post(context.Background(), s2, "test", "", []Part{{Type: "text/plain", Body: []byte("more")}}); err != nil {
		t.Fatal(err)
	}
	r.until(func() bool { return usage() == 560_000 })
	r.until(func() bool { i, err := r.h.Info(s2); return err == nil && i.Status == statefile.StatusClosed })
	// a touched session whose file does not read counts as it last read, and
	// is read again once it does
	r.h.touch(s2)
	if err := os.Rename(r.h.Sessions.Path(s2), r.h.Sessions.Path(s2)+".away"); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != 560_000 {
		t.Fatalf("usage with a touched session's file away: %d", got)
	}
	if err := os.Rename(r.h.Sessions.Path(s1), r.h.Sessions.Path(s2)); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != 420_000 {
		t.Fatalf("usage with the file back, as another: %d", got)
	}
	if err := os.Rename(r.h.Sessions.Path(s2), r.h.Sessions.Path(s1)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.h.Sessions.Path(s2)+".away", r.h.Sessions.Path(s2)); err != nil {
		t.Fatal(err)
	}
	r.h.touch(s2)
	if err := r.h.Delete(context.Background(), s1, "test"); err != nil {
		t.Fatal(err)
	}
	if got := usage(); got != 420_000 {
		t.Fatalf("usage after an earlier session was deleted: %d", got)
	}
}

// A change of only the workers reaches a live session that is stepping: the
// step under way keeps its workers, the next one is told of the new ones,
// and no session is made.
func TestWorkersMidStep(t *testing.T) {
	r := newRig(t)
	var second string
	r.step = func(body string) turn {
		if strings.Contains(body, "start") && !strings.Contains(body, "tool_result") {
			return turn{code: "wait", input: 1000}
		}
		second = body
		return turn{input: 1000}
	}
	a := r.create(AgentCreate{Config: r.conf(1001, 0, "w1")}, "web")
	if _, _, err := r.h.PostAgent(context.Background(), a.ID, "web", "", []Part{{Type: "text/plain", Body: []byte("start")}}); err != nil {
		t.Fatal(err)
	}
	<-r.started
	if err := r.h.SetAgentConfig(a.ID, "phone", *r.conf(1001, 0, "w1", "away")); err != nil {
		t.Fatal(err)
	}
	close(r.release)
	r.settled(a.ID)
	chain := r.chain(a.Sessions[0])
	conf, _ := config(chain)
	if got := r.sessions(a.ID); len(got) != 1 || len(conf.Workers) != 2 {
		t.Fatalf("sessions %v, workers %v", got, conf.Workers)
	}
	if !strings.Contains(second, "Workers (changed)") || !strings.Contains(second, "worker away") {
		t.Fatalf("the step after the change was not told of the workers: %s", second)
	}
}

// midrun-limit: a session that passes its limit between tool calls hands
// the task to the next one, whose first step finds the log on the worker.
func TestMidRunRollover(t *testing.T) {
	r := newRig(t)
	var atS2 string // the log of s1 on the worker when s2 first stepped
	a := r.create(AgentCreate{Config: r.conf(1000, 0, "w1", "away")}, "web")
	s1 := a.Sessions[0]
	log := filepath.Join(r.dir, "scratch", "logs", s1+".log")
	r.step = func(body string) turn {
		if strings.Contains(body, "hit its limit mid-task") {
			b, _ := os.ReadFile(log)
			atS2 = string(b)
			return turn{input: 100}
		}
		return turn{code: "wait", input: 1500}
	}
	if _, _, err := r.h.PostAgent(context.Background(), a.ID, "web", "", []Part{{Type: "text/plain", Body: []byte("start")}}); err != nil {
		t.Fatal(err)
	}
	<-r.started // the tool call is running: a post now is one no step has seen
	if s, _, err := r.h.PostAgent(context.Background(), a.ID, "phone", "", []Part{{Type: "text/plain", Body: []byte("also this")}}); err != nil || s != s1 {
		t.Fatalf("a post mid-step landed on %s: %v", s, err)
	}
	close(r.release)
	r.until(func() bool { return len(r.sessions(a.ID)) == 2 })
	r.settled(a.ID)
	s2 := r.sessions(a.ID)[1]

	chain := r.chain(s1)
	tip := chain[len(chain)-3:]
	if tip[0].Kind != statefile.Result || tip[1].Kind != statefile.System || tip[1].Source != "continue" || string(tip[1].Body) != s2 || tip[2].Kind != statefile.Error || !strings.Contains(string(tip[2].Body), "context 1500 exceeds limit 1000") {
		t.Fatalf("tip of s1: %v", tip)
	}
	if statefile.Derive(chain) != statefile.StatusFailed {
		t.Fatal("s1 would step again after a restart")
	}
	if !strings.Contains(atS2, `"source":"continue"`) || !strings.Contains(atS2, "exceeds limit") || !strings.Contains(atS2, "also this") {
		t.Fatalf("the log of s1 was not whole on the worker when s2 stepped: %d bytes", len(atS2))
	}
	msgs := r.chain(s2)
	msgs = slices.DeleteFunc(msgs, func(rec statefile.Record) bool { return rec.Kind != statefile.Message })
	if len(msgs) != 2 || string(msgs[0].Body) != "also this" || msgs[0].Via != "phone" || msgs[1].Via != "rollover" || !strings.Contains(string(msgs[1].Body), "/logs/"+s1+".log") {
		t.Fatalf("messages of s2: %v", msgs)
	}
	if got := r.bodies(s2, statefile.System, "continue"); len(got) != 1 || !strings.Contains(got[0], s1) {
		t.Fatalf("continue of s2: %q", got)
	}
	if _, ok := r.h.Holder(s1); ok {
		t.Fatal("s1 still holds the agent's key")
	}
	r.until(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.stopped) > 0 })
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.stopped, []string{s2 + " closed"}) {
		t.Fatalf("stops told: %v", r.stopped)
	}
	kill, write := slices.Index(r.events, "kill "+s1), slices.Index(r.events, "write /scratch/logs/"+s1+".log")
	if kill < 0 || write < kill {
		t.Fatalf("worker: %v", r.events)
	}
	// the end of s1's run does not write w1 the log it has, though a worker that is away has none
	if slices.Contains(r.events[write+1:], r.events[write]) {
		t.Fatalf("the log of s1 was written twice: %v", r.events)
	}
	if r.steps != 2 {
		t.Fatalf("%d model calls, want 2", r.steps)
	}
}

// A delete of an agent mid-step returns with its run over: no later step,
// queued kick or not, and no stop told of an agent that is gone.
func TestDeleteAgent(t *testing.T) {
	r := newRig(t)
	t.Cleanup(func() { close(r.release) })
	r.step = func(string) turn { return turn{code: "wait", input: 10} }
	a := r.create(AgentCreate{Config: r.conf(1000, 0, "w1")}, "web")
	s1 := a.Sessions[0]
	if _, _, err := r.h.PostAgent(context.Background(), a.ID, "web", "", []Part{{Type: "text/plain", Body: []byte("start")}}); err != nil {
		t.Fatal(err)
	}
	<-r.started
	r.h.Kick(context.Background(), s1) // one more pass is owed when this run ends
	s := r.h.session(s1)
	r.h.mu.Lock()
	run := s.done
	r.h.mu.Unlock()
	if err := r.h.DeleteAgent(context.Background(), a.ID, "web"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run:
	default:
		t.Fatal("the delete returned while the agent's session was still running")
	}
	time.Sleep(50 * time.Millisecond) // the kick the delete ends with finds a stopped session
	r.mu.Lock()
	steps, stopped := r.steps, slices.Clone(r.stopped)
	r.mu.Unlock()
	if steps != 1 || len(stopped) != 0 {
		t.Fatalf("after the delete: %d model calls, stops told %v", steps, stopped)
	}
	if _, ok := r.h.Holder(s1); ok {
		t.Fatal("a deleted agent's session still holds its key")
	}
	if _, err := r.h.Agent(a.ID); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("the agent after its delete: %v", err)
	}
	if st := statefile.Derive(r.chain(s1)); st == statefile.StatusOpen {
		t.Fatalf("the session of a deleted agent is %s: a restart would step it", st)
	}
}

// TestSessionVia: a session's Info says who made it, which is how a list of
// sessions tells an agent's from a plain one.
func TestSessionVia(t *testing.T) {
	r := newRig(t)
	a := r.create(AgentCreate{Config: r.conf(1000, 0)}, "web")
	if info, err := r.h.Info(a.live()); err != nil || info.Via != AgentVia+a.ID {
		t.Fatalf("an agent's session: via %q %v", info.Via, err)
	}
	id, err := r.h.Create(context.Background(), Create{Model: r.conf(1000, 0).Model, ContextLimit: 1000}, "tui")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := r.h.List()
	if err != nil || len(infos) != 2 {
		t.Fatalf("list: %+v %v", infos, err)
	}
	for _, info := range infos {
		if want := map[string]string{id: "tui", a.live(): AgentVia + a.ID}[info.ID]; info.Via != want {
			t.Fatalf("session %s: via %q, want %q", info.ID, info.Via, want)
		}
	}
	// ?plain=1 leaves the agent's out before the limit, though it is the newest
	time.Sleep(2 * time.Millisecond) // a record's time is in whole milliseconds
	r.post(a.ID, "newer")
	srv := httptest.NewServer(Handler(context.Background(), r.h))
	defer srv.Close()
	for q, want := range map[string]string{"?limit=1": a.live(), "?limit=1&plain=1": id} {
		resp, err := http.Get(srv.URL + "/sessions" + q)
		if err != nil {
			t.Fatal(err)
		}
		var got []Info
		err = json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Fatalf("GET /sessions%s: %+v %v, want %s", q, got, err, want)
		}
	}
}

// The /agents routes.
func TestAgentRoutes(t *testing.T) {
	r := newRig(t)
	srv := httptest.NewServer(Handler(context.Background(), r.h))
	defer srv.Close()
	do := func(code int, method, path, body string) string {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != code {
			t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, b, code)
		}
		return string(b)
	}
	conf, _ := json.Marshal(r.conf(1000, 0))
	var made struct{ ID, Session string }
	if err := json.Unmarshal([]byte(do(200, "POST", "/agents?via=web", `{"name":"boss","apps":["message"],"config":`+string(conf)+`}`)), &made); err != nil || made.ID == "" || made.Session == "" {
		t.Fatalf("create: %+v %v", made, err)
	}
	do(400, "POST", "/agents?via=web", `{"name":"no config"}`)
	time.Sleep(2 * time.Millisecond) // a record's time is to the millisecond, and the list is newest first
	sub := r.create(AgentCreate{Config: r.conf(1000, 0)}, "agent:"+made.ID)

	var info AgentInfo
	if err := json.Unmarshal([]byte(do(200, "GET", "/agents/"+made.ID, "")), &info); err != nil || info.Status != statefile.StatusClosed || info.CurrentSession != made.Session || info.Name != "boss" || info.Via != "web" || !slices.Equal(info.Sessions, []string{made.Session}) || info.Updated.IsZero() {
		t.Fatalf("get: %+v %v", info, err)
	}
	ids := func(query string) []string {
		t.Helper()
		var infos []AgentInfo
		if err := json.Unmarshal([]byte(do(200, "GET", "/agents"+query, "")), &infos); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, i := range infos {
			out = append(out, i.ID)
		}
		return out
	}
	if got := ids(""); !slices.Equal(got, []string{sub.ID, made.ID}) {
		t.Fatalf("list: %v", got)
	}
	if got := ids("?via=agent:" + made.ID); !slices.Equal(got, []string{sub.ID}) {
		t.Fatalf("list by via: %v", got)
	}
	if got := ids("?status=stepping"); len(got) != 0 {
		t.Fatalf("list by status: %v", got)
	}
	if got := ids("?limit=1"); !slices.Equal(got, []string{sub.ID}) {
		t.Fatalf("list with a limit: %v", got)
	}
	if got := ids("?after=" + sub.ID); !slices.Equal(got, []string{made.ID}) {
		t.Fatalf("list after: %v", got)
	}

	var posted struct {
		Session string
		N       int
	}
	if err := json.Unmarshal([]byte(do(200, "POST", "/agents/"+made.ID+"/records?via=web", "hello")), &posted); err != nil || posted.Session != made.Session || posted.N == 0 {
		t.Fatalf("post: %+v %v", posted, err)
	}
	r.settled(made.ID)
	do(400, "POST", "/agents/"+made.ID+"/records", "no via")
	do(204, "PUT", "/agents/"+made.ID+"/name?via=web", "chief")
	do(204, "PUT", "/agents/"+made.ID+"/apps?via=phone", `[]`)
	if got := do(200, "GET", "/agents/"+made.ID, ""); !strings.Contains(got, `"apps":[]`) {
		t.Fatalf("an agent with no apps: %s", got)
	}
	chain := r.chain(made.Session)
	if i := slices.IndexFunc(chain, func(rec statefile.Record) bool { return rec.Source == "apps" && rec.Via == "phone" }); i < 0 || !strings.HasSuffix(string(chain[i].Body), "none\n") {
		t.Fatalf("no apps record says who took the apps away: %v", r.bodies(made.Session, statefile.System, "apps"))
	}
	do(204, "PUT", "/agents/"+made.ID+"/apps?via=web", `["schedule"]`)
	do(400, "PUT", "/agents/"+made.ID+"/apps?via=web", `["nope"]`)
	do(204, "PUT", "/agents/"+made.ID+"/config?via=web", string(conf))
	do(400, "PUT", "/agents/"+made.ID+"/config?via=web", `{"model":"none/x","context_limit":5}`)
	do(204, "POST", "/agents/"+made.ID+"/stop?via=web", "")
	if got := do(200, "POST", "/agents/"+made.ID+"/retry", ""); !strings.Contains(got, `"status":"stepping"`) {
		t.Fatalf("retry: %s", got)
	}
	if a, _ := r.h.Agent(made.ID); a.Name != "chief" || !slices.Equal(a.Apps, []string{"schedule"}) {
		t.Fatalf("after the edits: %+v", a)
	}
	for _, c := range [][3]string{{"GET", "", ""}, {"POST", "/records?via=web", "x"}, {"POST", "/stop?via=web", ""}, {"POST", "/retry", ""}, {"PUT", "/name?via=web", "x"}, {"PUT", "/config?via=web", string(conf)}, {"PUT", "/apps?via=web", "[]"}} {
		do(404, c[0], "/agents/00000000"+c[1], c[2])
	}
	do(404, "DELETE", "/agents/00000000?via=web", "")
	do(204, "DELETE", "/agents/"+made.ID+"?via=web", "")
	do(404, "GET", "/agents/"+made.ID, "")
	if _, err := os.Stat(filepath.Join(r.h.Agents, made.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("the agent's file: %v", err)
	}
	if _, ok := r.h.Holder(made.Session); ok {
		t.Fatal("a deleted agent's session still holds its key")
	}
	do(200, "GET", "/sessions/"+made.Session, "") // the session's file stays
}

// An agent's live session is not deleted from under it; the sessions it has
// left are, and so is the live one once the agent is gone.
func TestDeleteLiveSession(t *testing.T) {
	r := newRig(t)
	t.Cleanup(func() { close(r.release) })
	ctx := context.Background()
	del := func(id string) (int, string) {
		w := httptest.NewRecorder()
		Handler(ctx, r.h).ServeHTTP(w, httptest.NewRequest("DELETE", "/sessions/"+id+"?via=test", nil))
		return w.Code, w.Body.String()
	}
	a := r.create(AgentCreate{Config: r.conf(1000, 0, "w1")}, "web")
	s1 := a.Sessions[0]
	before := len(r.chain(s1))
	if code, body := del(s1); code != http.StatusConflict || !strings.Contains(body, "agent "+a.ID) {
		t.Fatalf("delete of the live session: %d %s", code, body)
	}
	if after := len(r.chain(s1)); after != before {
		t.Fatalf("the refused delete wrote %d records to the session", after-before)
	}
	c := r.conf(1000, 0, "w1")
	c.Params = map[string]json.RawMessage{"temperature": json.RawMessage("0.5")}
	if err := r.h.SetAgentConfig(a.ID, "web", *c); err != nil {
		t.Fatal(err)
	}
	s2 := r.post(a.ID, "go on")
	if s2 == s1 {
		t.Fatal("no rollover")
	}
	if code, body := del(s1); code != http.StatusNoContent {
		t.Fatalf("delete of a session the agent left: %d %s", code, body)
	}
	if code, _ := del(s2); code != http.StatusConflict {
		t.Fatalf("delete of the new live session: %d", code)
	}
	// an adopted session is its agent's live one too, until the agent goes
	plain, err := r.h.Create(ctx, Create{Model: "fake/m1", ContextLimit: 1000}, "web")
	if err != nil {
		t.Fatal(err)
	}
	b := r.create(AgentCreate{Session: plain}, "web")
	if code, _ := del(plain); code != http.StatusConflict {
		t.Fatalf("delete of an adopted session: %d", code)
	}
	if err := r.h.DeleteAgent(ctx, b.ID, "web"); err != nil {
		t.Fatal(err)
	}
	if code, body := del(plain); code != http.StatusNoContent {
		t.Fatalf("delete after the agent's: %d %s", code, body)
	}
}

// A delete and an adoption of one session exclude each other: the delete
// never stops a session and then finds it an agent's.
func TestDeleteExcludesAdopt(t *testing.T) {
	r := newRig(t)
	t.Cleanup(func() { close(r.release) })
	ctx := context.Background()
	stepping, hold := make(chan struct{}), make(chan struct{})
	r.mu.Lock()
	r.step = func(string) turn { // a step that outlasts its stop, so the delete waits
		close(stepping)
		<-hold
		return turn{input: 10}
	}
	r.mu.Unlock()
	id, err := r.h.Create(ctx, Create{Model: "fake/m1", ContextLimit: 1000}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Post(ctx, id, "test", "", []Part{{Type: "text/plain", Body: []byte("go")}}); err != nil {
		t.Fatal(err)
	}
	<-stepping
	deleted := make(chan error, 1)
	go func() { deleted <- r.h.Delete(ctx, id, "test") }()
	s := r.h.session(id)
	r.until(func() bool {
		r.h.mu.Lock()
		defer r.h.mu.Unlock()
		return s.deleting
	})
	if a, err := r.h.CreateAgent(ctx, AgentCreate{Session: id}, "web"); err == nil {
		t.Fatalf("agent %s adopted a session that is being deleted", a.ID)
	}
	close(hold)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Sessions.Load(id); err == nil {
		t.Fatal("the session is still there")
	}
	if agents := r.h.ListAgents(func(Agent) bool { return true }); len(agents) != 0 {
		t.Fatalf("agents: %v", agents)
	}
}

// An id a caller made up reaches no file and keeps no lock.
func TestMadeUpIDs(t *testing.T) {
	r := newRig(t)
	t.Cleanup(func() { close(r.release) })
	ctx := context.Background()
	for _, s := range []string{"../agents/x", "..", "a/b", "ABCDEFGH", strings.Repeat("a", 300)} {
		if _, err := r.h.CreateAgent(ctx, AgentCreate{Session: s}, "web"); err == nil || !strings.Contains(err.Error(), "bad session id") {
			t.Fatalf("adopting %q: %v", s, err)
		}
	}
	for _, id := range []string{"00000000", "../x", ""} {
		if _, _, err := r.h.PostAgent(ctx, id, "agent:x", "", []Part{{Type: "text/plain", Body: []byte("hi")}}); !errors.Is(err, ErrNoAgent) {
			t.Fatalf("post to %q: %v", id, err)
		}
		if err := r.h.DeleteAgent(ctx, id, "web"); !errors.Is(err, ErrNoAgent) {
			t.Fatalf("delete of %q: %v", id, err)
		}
	}
	r.h.amu.Lock()
	n := len(r.h.alocks)
	r.h.amu.Unlock()
	if entries, _ := os.ReadDir(r.h.Agents); n != 0 || len(entries) != 0 {
		t.Fatalf("%d locks and %d files for agents that are not", n, len(entries))
	}
}
