package harness

import (
	"cmp"
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

// AgentVia starts the via of what an agent does, its id after it: record 1
// of each of its sessions, and what it posts or makes through an app.
const AgentVia = "agent:"

// Agent is agents/<id>.json: an identity that outlives a session. It holds
// the config of its next session, the apps it may use, and every session it
// has had, oldest first. The last one is live: the one a post lands on, and
// the only one whose key reaches the agent's apps. There is always one.
type Agent struct {
	ID       string      `json:"id"`
	Name     string      `json:"name,omitempty"`
	Via      string      `json:"via,omitempty"` // who made it: a connector, or agent:<id> for one an agent spawned
	Created  time.Time   `json:"created"`
	Config   AgentConfig `json:"config"`
	Apps     []string    `json:"apps"`
	Sessions []string    `json:"sessions"`
}

// AgentConfig is a session's config and the limit that is the agent's alone.
// Past ColdLimit a post that finds the prompt cache expired goes to a new
// session: the old one would be paid for again in full. Zero is no such limit.
type AgentConfig struct {
	statefile.Conf
	ColdLimit int `json:"cold_limit,omitempty"`
}

// AgentInfo is an agent as GET /agents shows it. Status, ContextUsed and
// Updated are its live session's. Usage is the agent's: the sum over every
// session it has had whose file is still there. The live session's own is
// in GET /sessions/{current_session}.
type AgentInfo struct {
	Agent
	Updated        time.Time        `json:"updated"`
	Status         statefile.Status `json:"status"`
	CurrentSession string           `json:"current_session"`
	ContextUsed    int              `json:"context_used"`
	Usage          statefile.Usage  `json:"usage"`
}

// AgentCreate is the body of POST /agents. With Session the agent adopts that
// session, and takes its config when it names none; without, Config is
// required and the first session is made from it.
type AgentCreate struct {
	Name    string       `json:"name,omitempty"`
	Config  *AgentConfig `json:"config,omitempty"`
	Apps    []string     `json:"apps,omitempty"`
	Session string       `json:"session,omitempty"`
}

// ErrNoAgent is an agent id no file has. ErrLive is a delete of the session
// an agent lives in.
var (
	ErrNoAgent = errors.New("no such agent")
	ErrLive    = errors.New("in use")
)

// What a rollover writes. continued is the system record a new session
// starts with; carry is the message that steps one made mid-task, when the
// model was not at a turn's end and nobody else will tell it to go on.
const (
	continued = "This session continues agent %s from session %s, which ended: %s. The whole of that session is in <scratch>/logs/%s.log on every worker: search it with rg for what you need, do not read it whole. The agent's sessions, oldest first: %s"
	carry     = "Session %s hit its limit mid-task (%s). Retrieve what you were doing from <scratch>/logs/%s.log and continue."
	appsHead  = "You are agent %s. Apps are services on the harness that this agent may call from any worker:\n  curl -H \"Authorization: Bearer $MAINPLANE_KEY\" $MAINPLANE_URL/apps/<app>/...\nRead an app's skill before its first use: GET $MAINPLANE_URL/skills/<app>, or mainplane skill <app>. The key works only while this is the agent's live session.\nApps:\n"
)

func (a Agent) live() string { return a.Sessions[len(a.Sessions)-1] }

// lockAgents takes amu with the maps ready.
func (h *Harness) lockAgents() {
	h.amu.Lock()
	if h.agents == nil {
		h.agents, h.live, h.alocks = map[string]Agent{}, map[string]string{}, map[string]*sync.Mutex{}
	}
}

// LoadAgents reads every agent's file into memory, and with them which agent
// each live session is of. Memory is a cache of the files; a file that does
// not read is logged and left out.
func (h *Harness) LoadAgents() error {
	if err := os.MkdirAll(h.Agents, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(h.Agents)
	if err != nil {
		return err
	}
	h.lockAgents()
	defer h.amu.Unlock()
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		var a Agent
		b, err := os.ReadFile(filepath.Join(h.Agents, e.Name()))
		if err == nil {
			err = json.Unmarshal(b, &a)
		}
		if err != nil || a.ID != id || len(a.Sessions) == 0 {
			log.Printf("agent %s: not read: %v", id, err)
			continue
		}
		h.agents[id], h.live[a.live()] = a, id
	}
	return nil
}

// Agent is the agent as its file has it now.
func (h *Harness) Agent(id string) (Agent, error) {
	h.lockAgents()
	defer h.amu.Unlock()
	a, ok := h.agents[id]
	if !ok {
		return a, fmt.Errorf("%w: %s", ErrNoAgent, id)
	}
	return a, nil
}

// Holder is the agent whose live session is session: none for a plain
// session, and none for a session its agent has moved on from.
func (h *Harness) Holder(session string) (Agent, bool) {
	h.lockAgents()
	defer h.amu.Unlock()
	a, ok := h.agents[h.live[session]]
	return a, ok
}

// holdAgent takes the agent's lock, under which one post or change at a time
// reads the agent and writes it, and returns the agent and the unlock.
func (h *Harness) holdAgent(id string) (Agent, func(), error) {
	h.lockAgents()
	mu, ok := h.alocks[id]
	if _, is := h.agents[id]; !ok && !is { // no lock is kept for an id a caller made up
		h.amu.Unlock()
		return Agent{}, nil, fmt.Errorf("%w: %s", ErrNoAgent, id)
	}
	if !ok {
		mu = &sync.Mutex{}
		h.alocks[id] = mu
	}
	h.amu.Unlock()
	mu.Lock()
	a, err := h.Agent(id)
	if err != nil {
		mu.Unlock()
	}
	return a, mu.Unlock, err
}

// save writes the agent's file whole, as a temporary file renamed over it,
// so a reader never sees half of one, then makes memory agree. The caller
// holds the agent's lock.
func (h *Harness) save(a Agent) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(h.Agents, a.ID+".json")
	if err := os.WriteFile(path+".tmp", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	h.lockAgents()
	defer h.amu.Unlock()
	if old, ok := h.agents[a.ID]; ok {
		delete(h.live, old.live())
	}
	h.agents[a.ID], h.live[a.live()] = a, a.ID
	return nil
}

// apps is the body of the apps system record: who the agent is, how it calls
// an app, and line 1 of each app's skill. An app the harness does not have
// is an error.
func (h *Harness) apps(a Agent) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, appsHead, a.ID)
	for _, name := range a.Apps {
		line, ok := "", false
		if h.Skill != nil {
			line, ok = h.Skill(name)
		}
		if !ok {
			return "", fmt.Errorf("unknown app %q", name)
		}
		fmt.Fprintf(&b, "%s: %s\n", name, line)
	}
	if len(a.Apps) == 0 {
		b.WriteString("none\n")
	}
	return b.String(), nil
}

// CreateAgent is POST /agents. The agent has a session from the start: one
// made from its config, which calls no model until a message comes, or the
// one it adopts. A session has one agent at most.
func (h *Harness) CreateAgent(ctx context.Context, c AgentCreate, via string) (Agent, error) {
	a := Agent{ID: statefile.NewID(), Name: c.Name, Via: via, Created: time.Now().UTC().Truncate(time.Millisecond), Apps: slices.Compact(slices.Sorted(slices.Values(c.Apps)))}
	if a.Apps == nil {
		a.Apps = []string{}
	}
	if err := checkName(a.Name); err != nil {
		return a, err
	}
	note, err := h.apps(a)
	if err != nil {
		return a, err
	}
	if c.Session == "" {
		if c.Config == nil {
			return a, errors.New("an agent takes a config, or a session to adopt")
		}
		a.Config = *c.Config
		id, err := h.next(ctx, a, statefile.NewID())
		if err != nil {
			return a, err
		}
		a.Sessions = []string{id}
		return a, h.save(a)
	}
	if !statefile.ValidID(c.Session) {
		return a, fmt.Errorf("bad session id %q", c.Session)
	}
	chain, err := h.Sessions.Load(c.Session)
	if err != nil {
		return a, err
	}
	if c.Config != nil {
		a.Config = *c.Config
	} else if a.Config.Conf, err = config(chain); err != nil {
		return a, err
	}
	if a.Config.Conf, _, err = h.valid(a.Config.Conf); err != nil {
		return a, err
	}
	// The claim and the checks are one step under amu, so two agents adopting
	// one session at once cannot both pass, and a delete of the session
	// either sees the claim before it stops anything or refuses this one.
	a.Sessions = []string{c.Session}
	s := h.session(c.Session)
	h.lockAgents()
	for _, o := range h.agents {
		if slices.Contains(o.Sessions, c.Session) {
			h.amu.Unlock()
			return a, fmt.Errorf("session %s is agent %s's", c.Session, o.ID)
		}
	}
	h.mu.Lock() // amu, then mu: never the reverse
	deleting := s.deleting
	h.mu.Unlock()
	if deleting {
		h.amu.Unlock()
		return a, fmt.Errorf("session %s is being stopped for a delete", c.Session)
	}
	h.agents[a.ID], h.live[c.Session] = a, a.ID
	h.amu.Unlock()
	if err = h.note(s, c.Session, system("apps", note)); err == nil {
		err = h.save(a)
	}
	if err != nil {
		h.lockAgents()
		delete(h.agents, a.ID)
		delete(h.live, c.Session)
		h.amu.Unlock()
	}
	return a, err
}

// next makes session id for the agent from its config, with the apps record
// every session of an agent starts with. It does not step.
func (h *Harness) next(ctx context.Context, a Agent, id string) (string, error) {
	note, err := h.apps(a)
	if err != nil {
		return "", err
	}
	c := a.Config
	if err := h.create(ctx, id, Create{Model: c.Model, ContextLimit: c.ContextLimit, Workers: c.Workers, Tools: c.Tools, Input: c.Input, Params: c.Params, Preset: c.Preset}, AgentVia+a.ID); err != nil {
		return "", err
	}
	return id, h.note(h.session(id), id, system("apps", note))
}

// AgentInfo is GET /agents/{id}.
func (h *Harness) AgentInfo(id string) (AgentInfo, error) {
	a, err := h.Agent(id)
	if err != nil {
		return AgentInfo{}, err
	}
	return h.agentInfo(a)
}

// agentInfo reads the live session's file. The sessions before it come from
// the index, so a list reads one file an agent, however long its chain.
func (h *Harness) agentInfo(a Agent) (AgentInfo, error) {
	i, err := h.Info(a.live())
	info := AgentInfo{Agent: a, Updated: i.Updated, Status: i.Status, CurrentSession: a.live(), ContextUsed: i.ContextUsed, Usage: i.Usage}
	for _, id := range a.Sessions[:len(a.Sessions)-1] {
		past, perr := h.cached(id)
		if perr != nil {
			continue // deleted through /sessions: what it cost is no longer known
		}
		info.Usage.Input += past.Usage.Input
		info.Usage.Output += past.Usage.Output
		info.Usage.CacheRead += past.Usage.CacheRead
		info.Usage.CacheWrite += past.Usage.CacheWrite
	}
	return info, err
}

// ListAgents is every agent keep says yes to, newest updated first. Only
// their live sessions' files are read. One whose live session does not read
// is logged and left out.
func (h *Harness) ListAgents(keep func(Agent) bool) []AgentInfo {
	h.lockAgents()
	agents := make([]Agent, 0, len(h.agents))
	for _, a := range h.agents {
		if keep(a) {
			agents = append(agents, a)
		}
	}
	h.amu.Unlock()
	infos := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		i, err := h.agentInfo(a)
		if err != nil {
			log.Printf("agent %s: %v", a.ID, err)
			continue
		}
		infos = append(infos, i)
	}
	// Two updated in one millisecond keep one order, so a page after an id is the same page each time.
	slices.SortFunc(infos, func(a, b AgentInfo) int { return cmp.Or(b.Updated.Compare(a.Updated), cmp.Compare(a.ID, b.ID)) })
	return infos
}

// core is what of a config a session cannot change and go on: all but the
// workers, which a config record changes, and the limit, which is the
// agent's to judge.
func core(c statefile.Conf) string {
	c.Workers, c.ContextLimit = nil, 0
	if c.Tools == "" {
		c.Tools = "default"
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// SetAgentConfig is PUT /agents/{id}/config. It makes no session: the next
// post does, if the live session cannot go on under the new config. When
// only the workers changed, the live session takes them in place and keeps
// its prompt cache.
func (h *Harness) SetAgentConfig(id, via string, c AgentConfig) error {
	var err error
	if c.Conf, _, err = h.valid(c.Conf); err != nil {
		return err
	}
	if c.ColdLimit < 0 {
		return errors.New("cold_limit is a count of tokens, or 0 for none")
	}
	a, unlock, err := h.holdAgent(id)
	if err != nil {
		return err
	}
	defer unlock()
	a.Config = c // the agent's file says what was asked; the live session takes what it can
	if err := h.save(a); err != nil {
		return err
	}
	chain, err := h.Sessions.Load(a.live())
	if err != nil {
		return err
	}
	conf, err := config(chain)
	if err != nil {
		return err
	}
	if core(conf) == core(c.Conf) && !slices.Equal(conf.Workers, c.Workers) && statefile.ContextUsed(chain) < c.ContextLimit {
		_, err = h.SetWorkers(a.live(), via, c.Workers)
	}
	return err
}

// SetAgentApps is PUT /agents/{id}/apps. The live session reads the new list
// as a system record at its next step, and its key reaches the new apps and
// no longer the ones taken away. That record says who changed them.
func (h *Harness) SetAgentApps(id, via string, apps []string) error {
	a, unlock, err := h.holdAgent(id)
	if err != nil {
		return err
	}
	defer unlock()
	if a.Apps = slices.Compact(slices.Sorted(slices.Values(apps))); a.Apps == nil {
		a.Apps = []string{} // a list in the file and every reply, as a new agent's is
	}
	text, err := h.apps(a)
	if err != nil {
		return err
	}
	if err := h.save(a); err != nil {
		return err
	}
	note := system("apps", text)
	note.Via = via
	return h.note(h.session(a.live()), a.live(), note)
}

// SetAgentName is PUT /agents/{id}/name. Empty removes the name. The
// agent's file holds what the agent is now and no history, so who named it
// goes to the log, as who deleted a session does.
func (h *Harness) SetAgentName(id, via, name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	a, unlock, err := h.holdAgent(id)
	if err != nil {
		return err
	}
	defer unlock()
	a.Name = name
	if err := h.save(a); err != nil {
		return err
	}
	log.Printf("agent %s: named %q by %s", id, name, via)
	return nil
}

// DeleteAgent removes the agent's file, then stops its live session and
// waits for the run to end, so nothing of the agent steps once this
// returns. The agent goes first: from then its key is refused, no post
// finds it, and a step that is handing the session over does not wait on
// an agent that waits on the step. Its sessions' files stay: they go
// through /sessions, and the live one is a plain session from here. A post
// to it that landed while it was held had its kick refused, so it is kicked
// once more: stopped, it steps only if such a post is at its tip.
func (h *Harness) DeleteAgent(ctx context.Context, id, via string) error {
	a, unlock, err := h.holdAgent(id)
	if err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(h.Agents, id+".json")); err == nil || errors.Is(err, fs.ErrNotExist) {
		err = nil
		h.lockAgents()
		delete(h.agents, id)
		delete(h.live, a.live())
		delete(h.alocks, id)
		h.amu.Unlock()
	}
	unlock()
	if err != nil {
		return err
	}
	release, err := h.halt(ctx, h.session(a.live()), via, nil)
	if err != nil {
		return err
	}
	release()
	h.Kick(context.WithoutCancel(ctx), a.live())
	log.Printf("agent %s: deleted by %s", id, via)
	return nil
}

// PostAgent is POST /agents/{id}/records: the post lands on the live
// session, or on a new one when the live one is idle and should not go on.
// A session that is stepping, or about to, takes the post as any session
// does, so one agent never steps two sessions at once. It returns the
// session the post landed on and its n.
func (h *Harness) PostAgent(ctx context.Context, id, via, key string, parts []Part) (string, int, error) {
	a, unlock, err := h.holdAgent(id)
	if err != nil {
		return "", 0, err
	}
	defer unlock()
	s := a.live()
	chain, err := h.Sessions.Load(s)
	if err != nil {
		return "", 0, err
	}
	held := key != "" && slices.ContainsFunc(chain, func(r statefile.Record) bool { return r.Kind == statefile.Message && r.IdempotencyKey == key })
	if st := h.status(s, chain); !held && (st == statefile.StatusClosed || st == statefile.StatusFailed) {
		if why := h.stale(a, chain); why != "" {
			h.copyLogs(ctx, h.session(s))
			next := statefile.NewID()
			if err := h.roll(ctx, &a, next, why); err != nil {
				return "", 0, err
			}
			if err := h.note(h.session(s), s, system("continue", next)); err != nil {
				return "", 0, err
			}
			s = next
		}
	}
	n, err := h.Post(ctx, s, via, key, parts)
	return s, n, err
}

// stale is why an idle live session should not take the next post, or empty:
// its config is not the agent's, it is at the limit, its prompt cache has
// expired and it is at the cold limit, or this harness no longer builds its
// request as the last step sent it.
func (h *Harness) stale(a Agent, chain []statefile.Record) string {
	conf, err := config(chain)
	if err != nil {
		return err.Error()
	}
	used := statefile.ContextUsed(chain)
	var cache Cache
	for _, r := range chain {
		if c, ok := cacheOf(r); ok {
			cache = c
		}
	}
	switch {
	case core(conf) != core(a.Config.Conf):
		return "the agent's config changed"
	case used >= a.Config.ContextLimit:
		return fmt.Sprintf("context %d reached limit %d", used, a.Config.ContextLimit)
	case a.Config.ColdLimit > 0 && used >= a.Config.ColdLimit && !time.Now().Before(cache.Until):
		return fmt.Sprintf("context %d reached cold limit %d and the prompt cache expired", used, a.Config.ColdLimit)
	}
	if p, model, err := h.provider(conf.Model); err == nil && rebuilt(p, model, conf, chain) != "" {
		return "a new harness version builds its request differently"
	}
	return ""
}

// roll makes session next the agent's live session, which revokes the key
// of the one before: made from the agent's config, told what it continues
// and where that session's log is. The caller holds the agent's lock.
func (h *Harness) roll(ctx context.Context, a *Agent, next, why string) error {
	prev := a.live()
	if _, err := h.next(ctx, *a, next); err != nil {
		return err
	}
	if err := h.note(h.session(next), next, system("continue", fmt.Sprintf(continued, a.ID, prev, why, prev, strings.Join(a.Sessions, ", ")))); err != nil {
		return err
	}
	a.Sessions = append(slices.Clone(a.Sessions), next)
	log.Printf("agent %s: session %s continues in %s: %s", a.ID, prev, next, why)
	return h.save(*a)
}

// handoff is a rollover mid-task, from inside the step that found the live
// session s past its limit or under a config no longer the agent's. s ends
// failed, the error at its tip, so no restart steps it again. The log of s
// is on its workers before the next session exists, so its first run finds
// it. The messages no step of s saw move to the next session, which is told
// to go on and kicked.
func (h *Harness) handoff(ctx context.Context, s *session, why string) (statefile.Status, error) {
	fail := func(text string) (statefile.Status, error) {
		_, err := h.append(s, errorRecord("", text))
		return statefile.StatusFailed, err
	}
	a, held := h.Holder(s.id)
	if !held {
		return fail(why)
	}
	a, unlock, err := h.holdAgent(a.ID)
	if err != nil || a.live() != s.id { // the agent moved on, or went, while this step waited
		if err == nil {
			unlock()
		}
		return fail(why)
	}
	defer unlock()
	next := statefile.NewID()
	if _, err := h.append(s, system("continue", next)); err != nil {
		return "", err
	}
	if st, err := fail(why + "; agent " + a.ID + " continues in session " + next); err != nil {
		return st, err
	}
	h.copyLogs(ctx, s)
	chain, err := h.Sessions.Load(s.id)
	if err != nil {
		return "", err
	}
	if err := h.roll(ctx, &a, next, why); err != nil {
		return "", err
	}
	upto := 0
	if last := statefile.LastStep(chain); last != nil {
		upto = last.Upto
	}
	ns := h.session(next)
	for _, r := range chain {
		if r.Kind == statefile.Message && r.N > upto {
			if err := h.note(ns, next, statefile.Record{Header: statefile.Header{Kind: statefile.Message, Type: r.Type, Via: r.Via, IdempotencyKey: r.IdempotencyKey}, Body: r.Body}); err != nil {
				return "", err
			}
		}
	}
	if _, err := h.Post(context.WithoutCancel(ctx), next, "rollover", "", []Part{{Type: "text/plain", Body: fmt.Appendf(nil, carry, s.id, why, s.id)}}); err != nil {
		return "", err
	}
	return statefile.StatusFailed, nil
}
