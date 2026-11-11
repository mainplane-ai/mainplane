package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Agent is agents/<id>.json: an identity that outlives its sessions. The last
// session is the live one, and there is always one. Every past session's
// config and records stay in its own file, so this holds only what is next.
type Agent struct {
	ID       string      `json:"id"`
	Name     string      `json:"name,omitempty"`
	Via      string      `json:"via"`
	Created  time.Time   `json:"created"`
	Config   AgentConfig `json:"config"`
	Apps     []string    `json:"apps"`
	Sessions []string    `json:"sessions"`
}

// AgentConfig is a session config and the cold limit: past it, an idle
// session whose prompt cache expired rolls over rather than pay to prefill.
type AgentConfig struct {
	statefile.Conf
	ColdLimit int `json:"cold_limit,omitempty"`
}

// Live is the agent's live session.
func (a Agent) Live() string { return a.Sessions[len(a.Sessions)-1] }

// AgentInfo is what GET /agents/{id} says: the file and its live session's
// state. Usage is every session's.
type AgentInfo struct {
	Agent
	Updated        time.Time        `json:"updated"`
	Status         statefile.Status `json:"status"`
	CurrentSession string           `json:"current_session"`
	ContextUsed    int              `json:"context_used"`
	Usage          statefile.Usage  `json:"usage"`
}

// NewAgent is the body of POST /agents. With Session, the agent adopts that
// session, its config from it when Config is nil; without, it starts one
// from Config.
type NewAgent struct {
	Name    string       `json:"name,omitempty"`
	Config  *AgentConfig `json:"config,omitempty"`
	Apps    []string     `json:"apps,omitempty"`
	Session string       `json:"session,omitempty"`
}

// agent is one agent in memory: the file's value, under mu, which every
// change and every rollover holds, so a message never lands on a session
// the agent is leaving. Gone is set by a delete, for a call that waited on
// mu meanwhile.
type agent struct {
	mu   sync.Mutex
	a    Agent
	gone bool
}

// Rollover reasons, as the continue note tells the next session.
const (
	whyConfig  = "ran on a config the agent no longer has"
	whyLimit   = "reached %d tokens of its %d token context limit"
	whyCold    = "was at %d tokens, over the %d token cold limit, with its prompt cache expired"
	whyRebuilt = "was built by another harness version"
	whyMidrun  = "Session %s %s (%s). Retrieve what you need from <scratch>/logs/%s.log and continue the task."

	whyMidLimit  = "hit its context limit mid-task"
	whyMidConfig = "stopped mid-task because the agent's config changed"
)

// LoadAgents reads agents/ and maps each live session to its agent.
func (h *Harness) LoadAgents() error {
	h.amu.Lock()
	defer h.amu.Unlock()
	h.byID, h.live = map[string]*agent{}, map[string]string{}
	if err := os.MkdirAll(h.AgentDir, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(h.AgentDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !statefile.ValidID(id) {
			continue
		}
		var a Agent
		b, err := os.ReadFile(filepath.Join(h.AgentDir, e.Name()))
		if err == nil {
			err = json.Unmarshal(b, &a)
		}
		if err == nil && len(a.Sessions) == 0 {
			err = errors.New("no session")
		}
		if err != nil {
			log.Printf("agent %s: %v", id, err)
			continue
		}
		h.byID[id], h.live[a.Live()] = &agent{a: a}, id
	}
	return nil
}

// save writes the agent through a file beside it and maps its live session,
// under ag.mu.
func (h *Harness) save(ag *agent, a Agent) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(h.AgentDir, a.ID+".json")
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	h.amu.Lock()
	defer h.amu.Unlock()
	if len(ag.a.Sessions) > 0 { // none yet on a new agent's first save
		delete(h.live, ag.a.Live())
	}
	ag.a, h.byID[a.ID], h.live[a.Live()] = a, ag, a.ID
	return nil
}

func (h *Harness) agent(id string) (*agent, error) {
	h.amu.Lock()
	defer h.amu.Unlock()
	if ag, ok := h.byID[id]; ok {
		return ag, nil
	}
	return nil, fmt.Errorf("no agent %s", id)
}

// lock is agent id with its mu held, unless a delete took it meanwhile.
func (h *Harness) lock(id string) (*agent, error) {
	ag, err := h.agent(id)
	if err != nil {
		return nil, err
	}
	ag.mu.Lock()
	if ag.gone {
		ag.mu.Unlock()
		return nil, fmt.Errorf("no agent %s", id)
	}
	return ag, nil
}

// GetAgent is the agent's file as it is now.
func (h *Harness) GetAgent(id string) (Agent, error) {
	ag, err := h.lock(id)
	if err != nil {
		return Agent{}, err
	}
	defer ag.mu.Unlock()
	return ag.a, nil
}

// LiveAgent is the agent whose live session is sid. A past session of an
// agent and a plain session have none.
func (h *Harness) LiveAgent(sid string) (Agent, bool) {
	h.amu.Lock()
	id, ok := h.live[sid]
	h.amu.Unlock()
	if !ok {
		return Agent{}, false
	}
	a, err := h.GetAgent(id)
	return a, err == nil && a.Live() == sid
}

// AllAgents is every agent's file, in no order.
func (h *Harness) AllAgents() []Agent {
	h.amu.Lock()
	ags := make([]*agent, 0, len(h.byID))
	for _, ag := range h.byID {
		ags = append(ags, ag)
	}
	h.amu.Unlock()
	out := make([]Agent, 0, len(ags))
	for _, ag := range ags {
		ag.mu.Lock()
		out = append(out, ag.a)
		ag.mu.Unlock()
	}
	return out
}

// OnStop calls fn each time an agent's live session stops, closed or failed.
// A session left by a rollover is not live, so a rollover calls nothing.
func (h *Harness) OnStop(fn func(agent string, st statefile.Status)) {
	h.amu.Lock()
	defer h.amu.Unlock()
	h.stops = append(h.stops, fn)
}

func (h *Harness) stopped(sid string, st statefile.Status) {
	a, ok := h.LiveAgent(sid)
	if !ok {
		return
	}
	h.amu.Lock()
	fns := slices.Clone(h.stops)
	h.amu.Unlock()
	for _, fn := range fns {
		fn(a.ID, st)
	}
}

// AgentInfo is the agent with its live session's state.
func (h *Harness) AgentInfo(id string) (AgentInfo, error) {
	a, err := h.GetAgent(id)
	if err != nil {
		return AgentInfo{}, err
	}
	info := AgentInfo{Agent: a, CurrentSession: a.Live()}
	for _, sid := range a.Sessions {
		si, err := h.cached(sid)
		if err != nil {
			continue // a past session deleted through /sessions
		}
		info.Usage.Input += si.Usage.Input
		info.Usage.Output += si.Usage.Output
		info.Usage.CacheRead += si.Usage.CacheRead
		info.Usage.CacheWrite += si.Usage.CacheWrite
		if sid == a.Live() {
			info.Updated, info.ContextUsed = si.Updated, si.ContextUsed
			info.Status = si.Status
			if h.running(sid) {
				info.Status = statefile.StatusStepping
			}
		}
	}
	return info, nil
}

// cached is the index's entry for a session no write touched since, else
// the file's.
func (h *Harness) cached(id string) (Info, error) {
	h.lockIndex()
	info, ok := h.index[id]
	ok = ok && !h.dirty[id]
	h.imu.Unlock()
	if ok {
		return info, nil
	}
	return h.Info(id)
}

// checkAgent normalizes a config as Create would store it, so a comparison
// with a session's config sees only real changes, and checks it as Create
// would, so a rollover never meets a config it cannot start. It checks the
// apps too.
func (h *Harness) checkAgent(c *AgentConfig, apps []string) error {
	if c.Tools == "" {
		c.Tools = "default"
	}
	if c.Workers == nil {
		c.Workers = []statefile.Worker{}
	}
	if err := h.check(c.Conf); err != nil {
		return err
	}
	if c.ColdLimit < 0 {
		return errors.New("cold_limit is not negative")
	}
	for _, n := range apps {
		if _, ok := h.Apps.Get(n); !ok {
			return fmt.Errorf("no app %s; apps: %s", n, strings.Join(h.Apps.Names(), ", "))
		}
	}
	return nil
}

// CreateAgent is POST /agents.
func (h *Harness) CreateAgent(ctx context.Context, n NewAgent, via string) (Agent, error) {
	if err := checkName(n.Name); err != nil {
		return Agent{}, err
	}
	a := Agent{ID: statefile.NewID(), Name: n.Name, Via: via, Created: time.Now().UTC(), Apps: n.Apps}
	if a.Apps == nil {
		a.Apps = []string{}
	}
	if n.Session != "" {
		if !statefile.ValidID(n.Session) {
			return Agent{}, fmt.Errorf("bad session id %q", n.Session)
		}
		chain, err := h.Sessions.Load(n.Session)
		if err != nil {
			return Agent{}, err
		}
		conf, err := config(chain)
		if err != nil {
			return Agent{}, err
		}
		if n.Config == nil {
			n.Config = &AgentConfig{Conf: conf}
		}
	}
	if n.Config == nil {
		return Agent{}, errors.New("an agent needs a config or a session")
	}
	a.Config = *n.Config
	if err := h.checkAgent(&a.Config, a.Apps); err != nil {
		return Agent{}, err
	}
	ag := &agent{a: a}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if n.Session == "" {
		sid := statefile.NewID()
		if err := h.start(ctx, sid, a); err != nil {
			return Agent{}, err
		}
		a.Sessions = []string{sid}
	} else {
		// One adoption at a time, through the save that maps the session,
		// so two never take one session. Agents are read one lock at a
		// time, never under amu: save takes an agent's mu, then amu.
		h.adopt.Lock()
		defer h.adopt.Unlock()
		for _, other := range h.AllAgents() {
			if slices.Contains(other.Sessions, n.Session) {
				return Agent{}, fmt.Errorf("session %s belongs to agent %s", n.Session, other.ID)
			}
		}
		a.Sessions = []string{n.Session}
		if err := h.note(h.session(n.Session), n.Session, h.appsNote(a)); err != nil {
			return Agent{}, err
		}
	}
	return a, h.save(ag, a)
}

// start creates session sid of agent a, its apps note and extra records
// after the worker list.
func (h *Harness) start(ctx context.Context, sid string, a Agent, extra ...statefile.Record) error {
	c := a.Config
	return h.create(ctx, sid, Create{Model: c.Model, ContextLimit: c.ContextLimit, Workers: c.Workers, Tools: c.Tools, Input: c.Input, Params: c.Params, Preset: c.Preset}, "agent:"+a.ID, append([]statefile.Record{h.appsNote(a)}, extra...))
}

// appsNote is the apps system record: who the session is, and a line per app.
func (h *Harness) appsNote(a Agent) statefile.Record {
	var b strings.Builder
	fmt.Fprintf(&b, "This session is agent %s.", a.ID)
	if len(a.Apps) == 0 {
		b.WriteString(" It has no apps.")
		return system("apps", b.String())
	}
	b.WriteString(" Its apps answer at $MAINPLANE_URL/apps/<name> with the header Authorization: Bearer $MAINPLANE_KEY; both variables are set in every interpreter and job. Read an app's whole skill before using it: GET $MAINPLANE_URL/skills/<name>, or mainplane skill <name>.\n")
	for _, n := range a.Apps {
		summary := "not installed on this harness now"
		if app, ok := h.Apps.Get(n); ok {
			summary = app.Summary()
		}
		fmt.Fprintf(&b, "- %s: %s\n", n, summary)
	}
	return system("apps", b.String())
}

// continueNote opens the next session: why the last one ended, where its log
// is, and the lineage.
func continueNote(a Agent, from, why string) statefile.Record {
	return system("continue", fmt.Sprintf("Agent %s continues from session %s, which %s. Its whole record is at <scratch>/logs/%s.log on every worker of this session, one JSON header line per record and then its body: search it with rg for what the task needs, rather than reading it whole. Sessions of this agent, oldest first: %s.", a.ID, from, why, from, strings.Join(a.Sessions, ", ")))
}

// sameConf is whether two configs build the same requests: model, tools,
// input, params and preset. Workers and limits change in place.
func sameConf(a, b statefile.Conf) bool {
	a.Workers, b.Workers, a.ContextLimit, b.ContextLimit = nil, nil, 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// stale is why an idle session must not take the agent's next message, or
// empty: Rule 2.
func (h *Harness) stale(a Agent, chain []statefile.Record) string {
	conf, err := config(chain)
	if err != nil || !sameConf(conf, a.Config.Conf) {
		return whyConfig
	}
	used := statefile.ContextUsed(chain)
	if used >= a.Config.ContextLimit {
		return fmt.Sprintf(whyLimit, used, a.Config.ContextLimit)
	}
	if a.Config.ColdLimit > 0 && used >= a.Config.ColdLimit && cold(chain) {
		return fmt.Sprintf(whyCold, used, a.Config.ColdLimit)
	}
	if p, model, err := h.provider(conf.Model); err == nil && rebuilt(p, model, conf, chain) != "" {
		return whyRebuilt
	}
	return ""
}

// cold is whether the session's prompt cache has expired. A session whose
// vendor states no window counts as cold: nothing says it is warm.
func cold(chain []statefile.Record) bool {
	var until time.Time
	for _, r := range chain {
		if c, ok := cacheOf(r); ok {
			until = c.Until
		}
	}
	return !time.Now().Before(until)
}

// PostAgent is POST /agents/{id}/records: Rule 2. A stepping session takes
// the post as any post mid-step, so one agent never steps two sessions. An
// idle one that is stale rolls over first, and the post lands on the next.
func (h *Harness) PostAgent(ctx context.Context, id, via, key string, parts []Part) (string, int, error) {
	ag, err := h.lock(id)
	if err != nil {
		return "", 0, err
	}
	defer ag.mu.Unlock()
	a := ag.a
	sid := a.Live()
	chain, err := h.Sessions.Load(sid)
	if err != nil {
		return "", 0, err
	}
	if st := h.status(sid, chain); st == statefile.StatusClosed || st == statefile.StatusFailed {
		if why := h.stale(a, chain); why != "" {
			if sid, err = h.rollover(ctx, ag, why); err != nil {
				return "", 0, err
			}
		} else if conf, _ := config(chain); !slices.Equal(conf.Workers, a.Config.Workers) {
			if _, err := h.SetWorkers(sid, via, a.Config.Workers); err != nil {
				return "", 0, err
			}
		}
	}
	n, err := h.Post(ctx, sid, via, key, parts)
	return sid, n, err
}

// rollover moves an idle agent to a new session, under ag.mu: the log goes
// to the workers, the new session is made and becomes live, which revokes
// the old one's key, and the old one says where its agent went.
func (h *Harness) rollover(ctx context.Context, ag *agent, why string) (string, error) {
	a, from := ag.a, ag.a.Live()
	s := h.session(from)
	h.copyLogs(ctx, s)
	next := statefile.NewID()
	if err := h.start(ctx, next, a, continueNote(a, from, why)); err != nil {
		return "", err
	}
	a.Sessions = append(slices.Clone(a.Sessions), next)
	if err := h.save(ag, a); err != nil {
		return "", err
	}
	log.Printf("agent %s: %s -> %s: session %s", a.ID, from, next, why)
	return next, h.note(s, from, system("continue", next))
}

// handoff is Rule 3, from Step on the live session of an agent past its
// limit or off its config mid-task, with the step's file held. The session
// ends failed, its tip an error saying stop so Resume never kicks it again; its log is
// on every worker before the next session's first step; messages the model
// never saw move with the task. False when the session is no agent's.
func (h *Harness) handoff(ctx context.Context, s *session, id, stop, why string) (bool, error) {
	a, ok := h.LiveAgent(id)
	if !ok {
		return false, nil
	}
	ag, err := h.lock(a.ID)
	if err != nil {
		return false, nil // deleted since
	}
	defer ag.mu.Unlock()
	if a = ag.a; a.Live() != id {
		return false, nil
	}
	next := statefile.NewID()
	if _, err := h.append(s, system("continue", next)); err != nil {
		return true, err
	}
	if _, err := h.append(s, errorRecord("", stop)); err != nil {
		return true, err
	}
	chain, err := h.Sessions.Load(id) // with every post that came before ag.mu
	if err != nil {
		return true, err
	}
	h.copyLogs(ctx, s)
	if err := h.start(ctx, next, a, continueNote(a, id, why)); err != nil {
		return true, err
	}
	a.Sessions = append(slices.Clone(a.Sessions), next)
	if err := h.save(ag, a); err != nil {
		return true, err
	}
	upto := 0
	if st := statefile.LastStep(chain); st != nil {
		upto = st.Upto
	}
	carry := []statefile.Record{{Header: statefile.Header{Kind: statefile.Message, Type: "text/plain", Via: "harness"}, Body: fmt.Appendf(nil, whyMidrun, id, why, stop, id)}}
	for _, r := range chain {
		if r.Kind == statefile.Message && r.N > upto {
			carry = append(carry, statefile.Record{Header: statefile.Header{Kind: statefile.Message, Type: r.Type, Via: r.Via, IdempotencyKey: r.IdempotencyKey}, Body: r.Body})
		}
	}
	sn := h.session(next)
	for _, r := range carry {
		if err := h.note(sn, next, r); err != nil {
			return true, err
		}
	}
	log.Printf("agent %s: %s -> %s: session %s (%s)", a.ID, id, next, why, stop)
	h.Kick(context.WithoutCancel(ctx), next)
	return true, nil
}

// SetAgentConfig is PUT /agents/{id}/config: Rule 1. No session is made; the
// next post rolls over if the config needs it. A change of workers alone
// goes into the live session at once, unless that session is done.
func (h *Harness) SetAgentConfig(id, via string, c AgentConfig) error {
	ag, err := h.lock(id)
	if err != nil {
		return err
	}
	defer ag.mu.Unlock()
	a := ag.a
	if err := h.checkAgent(&c, a.Apps); err != nil {
		return err
	}
	a.Config = c
	if err := h.save(ag, a); err != nil {
		return err
	}
	chain, err := h.Sessions.Load(a.Live())
	if err != nil {
		return err
	}
	if conf, _ := config(chain); sameConf(conf, c.Conf) && !slices.Equal(conf.Workers, c.Workers) && statefile.ContextUsed(chain) < c.ContextLimit {
		_, err = h.SetWorkers(a.Live(), via, c.Workers)
	}
	return err
}

// SetAgentApps is PUT /agents/{id}/apps: the live session reads the new list
// in an apps note at its tip, so its cached prefix holds, and its key loses
// every app taken away at once.
func (h *Harness) SetAgentApps(id string, apps []string) error {
	ag, err := h.lock(id)
	if err != nil {
		return err
	}
	defer ag.mu.Unlock()
	a := ag.a
	if apps == nil {
		apps = []string{}
	}
	if err := h.checkAgent(&a.Config, apps); err != nil {
		return err
	}
	a.Apps = apps
	if err := h.save(ag, a); err != nil {
		return err
	}
	return h.note(h.session(a.Live()), a.Live(), h.appsNote(a))
}

// SetAgentName is PUT /agents/{id}/name; empty removes the name.
func (h *Harness) SetAgentName(id, n string) error {
	if err := checkName(n); err != nil {
		return err
	}
	ag, err := h.lock(id)
	if err != nil {
		return err
	}
	defer ag.mu.Unlock()
	a := ag.a
	a.Name = n
	return h.save(ag, a)
}

// StopAgent stops the live session.
func (h *Harness) StopAgent(ctx context.Context, id, via string) error {
	a, err := h.GetAgent(id)
	if err != nil {
		return err
	}
	return h.Stop(ctx, a.Live(), via)
}

// RetryAgent retries the live session.
func (h *Harness) RetryAgent(ctx context.Context, id string) error {
	a, err := h.GetAgent(id)
	if err != nil {
		return err
	}
	h.Retry(ctx, a.Live())
	return nil
}

// DeleteAgent removes the file, then stops the live session and waits for
// its step to end. Unmapped first, the session is no agent's: its key is
// revoked, no post reaches it, and its stop calls no OnStop. The sessions'
// files stay: /sessions deletes those.
func (h *Harness) DeleteAgent(ctx context.Context, id, via string) error {
	ag, err := h.lock(id)
	if err != nil {
		return err
	}
	live := ag.a.Live()
	if err := os.Remove(filepath.Join(h.AgentDir, id+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		ag.mu.Unlock()
		return err
	}
	ag.gone = true
	h.amu.Lock()
	delete(h.byID, id)
	delete(h.live, live)
	h.amu.Unlock()
	ag.mu.Unlock()
	log.Printf("agent %s: deleted by %s", id, via)
	if err := h.Stop(ctx, live, via); err != nil {
		log.Printf("agent %s: stop of %s after delete: %v", id, live, err)
	}
	s := h.session(live)
	h.mu.Lock()
	running, done := s.running, s.done
	h.mu.Unlock()
	if running {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
