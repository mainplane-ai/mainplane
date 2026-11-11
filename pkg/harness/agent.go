package harness

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

type (
	AgentConfig = apps.AgentConfig
	AgentInfo   = apps.AgentInfo
	AgentCreate = apps.AgentCreate
)

// agentFile is the on-disk JSON shape of agents/<id>.json.
type agentFile struct {
	ID       string      `json:"id"`
	Name     string      `json:"name,omitempty"`
	Via      string      `json:"via,omitempty"`
	Created  time.Time   `json:"created"`
	Config   AgentConfig `json:"config"`
	Apps     []string    `json:"apps"`
	Sessions []string    `json:"sessions"`
}

type agentEntry struct {
	mu sync.Mutex
	a  agentFile
}

func (h *Harness) agentsDir() string {
	if h.AgentsDir != "" {
		return h.AgentsDir
	}
	return filepath.Join(filepath.Dir(h.Sessions.Dir), "agents")
}

func (h *Harness) agentPath(id string) string {
	return filepath.Join(h.agentsDir(), id+".json")
}

// initAgents loads agents/*.json into memory once.
func (h *Harness) initAgents() error {
	h.amu.Lock()
	defer h.amu.Unlock()
	if h.agentsLoaded {
		return nil
	}
	h.agentMap = map[string]*agentEntry{}
	h.liveAgent = map[string]string{}
	h.ownAgent = map[string]string{}
	dir := h.agentsDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !statefile.ValidID(id) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		var af agentFile
		if err := json.Unmarshal(b, &af); err != nil || len(af.Sessions) == 0 {
			continue
		}
		if af.Apps == nil {
			af.Apps = []string{}
		}
		h.agentMap[af.ID] = &agentEntry{a: af}
		for _, sid := range af.Sessions {
			h.ownAgent[sid] = af.ID
		}
		h.liveAgent[af.Sessions[len(af.Sessions)-1]] = af.ID
	}
	h.agentsLoaded = true
	return nil
}

func (h *Harness) saveAgentFile(af agentFile) error {
	if len(af.Sessions) == 0 {
		return errors.New("agent must have at least one session")
	}
	if af.Apps == nil {
		af.Apps = []string{}
	}
	if af.Config.Workers == nil {
		af.Config.Workers = []statefile.Worker{}
	}
	b, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return err
	}
	dir := h.agentsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := h.agentPath(af.ID)
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func (h *Harness) lookupAgentEntry(id string) (*agentEntry, error) {
	if !statefile.ValidID(id) {
		return nil, fmt.Errorf("bad agent id %q", id)
	}
	if err := h.initAgents(); err != nil {
		return nil, err
	}
	h.amu.Lock()
	e, ok := h.agentMap[id]
	h.amu.Unlock()
	if !ok {
		return nil, fmt.Errorf("agent %s: %w", id, fs.ErrNotExist)
	}
	return e, nil
}

func (h *Harness) liveEntryForSession(sid string) (*agentEntry, bool) {
	if err := h.initAgents(); err != nil {
		return nil, false
	}
	h.amu.Lock()
	aid, ok := h.liveAgent[sid]
	var e *agentEntry
	if ok {
		e = h.agentMap[aid]
	}
	h.amu.Unlock()
	return e, e != nil
}

func (h *Harness) validateAgentConfig(cfg *AgentConfig) error {
	p, _, err := h.provider(cfg.Model)
	if err != nil {
		return err
	}
	if cfg.ContextLimit <= 0 {
		return errors.New("context_limit must be set")
	}
	if cfg.ColdLimit < 0 {
		return errors.New("cold_limit cannot be negative")
	}
	for _, t := range cfg.Input {
		if !p.Envelope.Accepts(t) {
			return fmt.Errorf("%s does not take %s as input", p.Envelope.Name(), t)
		}
	}
	for k := range cfg.Params {
		if p.Envelope.Owns(k) {
			return fmt.Errorf("%s builds %s from the session, a param cannot set it", p.Envelope.Name(), k)
		}
	}
	if cfg.Tools == "" {
		cfg.Tools = "default"
	}
	if Tools(cfg.Tools, nil) == nil {
		return fmt.Errorf("unknown tools %q; sets: default, patch", cfg.Tools)
	}
	if cfg.Workers == nil {
		cfg.Workers = []statefile.Worker{}
	}
	return nil
}

func cleanAppList(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, a := range list {
		if !apps.ValidAppName(a) {
			return nil, fmt.Errorf("bad app name %q", a)
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (h *Harness) appsSummary(list []string, changed bool) string {
	if h.Apps != nil {
		return h.Apps.Summary(list, changed)
	}
	lead := "Apps:\n"
	if changed {
		lead = "Apps (changed):\n"
	}
	if len(list) == 0 {
		if changed {
			return "Apps (changed): none\n"
		}
		return "Apps: none\n"
	}
	var b strings.Builder
	b.WriteString(lead)
	for _, a := range list {
		fmt.Fprintf(&b, "- %s\n", a)
	}
	return b.String()
}

func (h *Harness) createAgentSession(ctx context.Context, sid, agentID, agentName string, cfg AgentConfig, appList []string, prev string, lineage []string) error {
	extra := []statefile.Record{system("apps", h.appsSummary(appList, false))}
	if prev != "" {
		note := fmt.Sprintf("Continuing agent %s from session %s (sessions: %s). Past session logs are on workers at <scratch>/logs/%s.log and can be searched with rg.", agentID, prev, strings.Join(lineage, ", "), prev)
		extra = append(extra, system("continue", note))
	}
	return h.createSession(ctx, sid, Create{
		Model:        cfg.Model,
		ContextLimit: cfg.ContextLimit,
		Workers:      cfg.Workers,
		Tools:        cfg.Tools,
		Input:        cfg.Input,
		Params:       cfg.Params,
		Preset:       cfg.Preset,
		Name:         agentName,
	}, "agent:"+agentID, extra...)
}

func confToAgentConfig(c statefile.Conf) AgentConfig {
	ws := c.Workers
	if ws == nil {
		ws = []statefile.Worker{}
	}
	return AgentConfig{
		Model:        c.Model,
		Tools:        c.Tools,
		Workers:      ws,
		ContextLimit: c.ContextLimit,
		Input:        c.Input,
		Params:       c.Params,
		Preset:       c.Preset,
	}
}

func sameParams(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		var ca, cb bytes.Buffer
		if json.Compact(&ca, va) != nil || json.Compact(&cb, vb) != nil || !bytes.Equal(ca.Bytes(), cb.Bytes()) {
			return false
		}
	}
	return true
}

func sameCoreConfig(sc statefile.Conf, ac AgentConfig) bool {
	tools := ac.Tools
	if tools == "" {
		tools = "default"
	}
	return sc.Model == ac.Model &&
		sc.Tools == tools &&
		slices.Equal(sc.Input, ac.Input) &&
		sameParams(sc.Params, ac.Params) &&
		sc.Preset == ac.Preset
}

// CreateAgent implements one-trip and two-trip POST /agents.
func (h *Harness) CreateAgent(ctx context.Context, c AgentCreate, via string) (string, string, error) {
	if err := h.initAgents(); err != nil {
		return "", "", err
	}
	if err := checkName(c.Name); err != nil {
		return "", "", err
	}
	appList, err := cleanAppList(c.Apps)
	if err != nil {
		return "", "", err
	}
	aid := statefile.NewID()

	if c.Session != "" {
		return h.adoptAgentSession(aid, c, appList, via)
	}
	if c.Config == nil {
		return "", "", errors.New("config is required when session is omitted")
	}
	cfg := *c.Config
	if err := h.validateAgentConfig(&cfg); err != nil {
		return "", "", err
	}
	sid := statefile.NewID()
	if err := h.createAgentSession(ctx, sid, aid, c.Name, cfg, appList, "", nil); err != nil {
		return "", "", err
	}
	af := agentFile{
		ID:       aid,
		Name:     c.Name,
		Via:      via,
		Created:  time.Now().UTC().Truncate(time.Millisecond),
		Config:   cfg,
		Apps:     appList,
		Sessions: []string{sid},
	}
	if err := h.saveAgentFile(af); err != nil {
		return "", "", err
	}
	h.amu.Lock()
	h.agentMap[aid] = &agentEntry{a: af}
	h.liveAgent[sid] = aid
	h.ownAgent[sid] = aid
	h.amu.Unlock()
	return aid, sid, nil
}

func (h *Harness) adoptAgentSession(aid string, c AgentCreate, appList []string, via string) (string, string, error) {
	sid := c.Session
	if !statefile.ValidID(sid) {
		return "", "", fmt.Errorf("bad session id %q", sid)
	}
	chain, err := h.Sessions.Load(sid)
	if err != nil {
		return "", "", err
	}
	sConf, err := config(chain)
	if err != nil {
		return "", "", err
	}
	cfg := confToAgentConfig(sConf)
	if c.Config != nil {
		cfg = *c.Config
		if err := h.validateAgentConfig(&cfg); err != nil {
			return "", "", err
		}
	}
	h.amu.Lock()
	if owner := h.ownAgent[sid]; owner != "" {
		h.amu.Unlock()
		return "", "", fmt.Errorf("session %s already belongs to agent %s", sid, owner)
	}
	// Reserve sid before writing so concurrent adoptions cannot race.
	h.ownAgent[sid] = aid
	h.amu.Unlock()

	if err := h.note(h.session(sid), sid, system("apps", h.appsSummary(appList, false))); err != nil {
		h.amu.Lock()
		delete(h.ownAgent, sid)
		h.amu.Unlock()
		return "", "", err
	}
	if c.Name != "" {
		_, _ = h.SetName(sid, cmp.Or(via, "agent:"+aid), c.Name)
	}
	if c.Config != nil && sameCoreConfig(sConf, cfg) && !slices.Equal(sConf.Workers, cfg.Workers) {
		_, _ = h.SetWorkers(sid, cmp.Or(via, "agent:"+aid), cfg.Workers)
	}
	af := agentFile{
		ID:       aid,
		Name:     c.Name,
		Via:      via,
		Created:  time.Now().UTC().Truncate(time.Millisecond),
		Config:   cfg,
		Apps:     appList,
		Sessions: []string{sid},
	}
	if err := h.saveAgentFile(af); err != nil {
		h.amu.Lock()
		delete(h.ownAgent, sid)
		h.amu.Unlock()
		return "", "", err
	}
	h.amu.Lock()
	h.agentMap[aid] = &agentEntry{a: af}
	h.liveAgent[sid] = aid
	h.amu.Unlock()
	return aid, sid, nil
}

func (h *Harness) agentInfoFromFile(af agentFile) (AgentInfo, error) {
	cur := af.Sessions[len(af.Sessions)-1]
	live, err := h.Info(cur)
	if err != nil {
		return AgentInfo{}, err
	}
	var total statefile.Usage
	for _, sid := range af.Sessions {
		if sid == cur {
			total.Input += live.Usage.Input
			total.Output += live.Usage.Output
			total.CacheRead += live.Usage.CacheRead
			total.CacheWrite += live.Usage.CacheWrite
			continue
		}
		if in, err := h.Info(sid); err == nil {
			total.Input += in.Usage.Input
			total.Output += in.Usage.Output
			total.CacheRead += in.Usage.CacheRead
			total.CacheWrite += in.Usage.CacheWrite
		}
	}
	updated := live.Updated
	if af.Created.After(updated) {
		updated = af.Created
	}
	return AgentInfo{
		ID:             af.ID,
		Name:           af.Name,
		Via:            af.Via,
		Created:        af.Created,
		Updated:        updated,
		Status:         live.Status,
		Config:         af.Config,
		Apps:           slices.Clone(af.Apps),
		Sessions:       slices.Clone(af.Sessions),
		CurrentSession: cur,
		ContextUsed:    live.ContextUsed,
		Usage:          total,
	}, nil
}

// GetAgent returns AgentInfo for id.
func (h *Harness) GetAgent(id string) (AgentInfo, error) {
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return AgentInfo{}, err
	}
	e.mu.Lock()
	af := e.a
	e.mu.Unlock()
	return h.agentInfoFromFile(af)
}

// LiveAgent returns the agent whose live session is sessionID.
func (h *Harness) LiveAgent(sessionID string) (AgentInfo, bool) {
	e, ok := h.liveEntryForSession(sessionID)
	if !ok {
		return AgentInfo{}, false
	}
	e.mu.Lock()
	af := e.a
	e.mu.Unlock()
	if len(af.Sessions) == 0 || af.Sessions[len(af.Sessions)-1] != sessionID {
		return AgentInfo{}, false
	}
	in, err := h.agentInfoFromFile(af)
	return in, err == nil
}

// AgentHasApp reports whether agentID exists and currently holds app.
func (h *Harness) AgentHasApp(agentID, app string) bool {
	e, err := h.lookupAgentEntry(agentID)
	if err != nil {
		return false
	}
	e.mu.Lock()
	has := slices.Contains(e.a.Apps, app)
	e.mu.Unlock()
	return has
}

// ListAgents returns every agent, newest updated first.
func (h *Harness) ListAgents() ([]AgentInfo, error) {
	if err := h.initAgents(); err != nil {
		return nil, err
	}
	h.amu.Lock()
	entries := make([]*agentEntry, 0, len(h.agentMap))
	for _, e := range h.agentMap {
		entries = append(entries, e)
	}
	h.amu.Unlock()
	out := make([]AgentInfo, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		af := e.a
		e.mu.Unlock()
		if in, err := h.agentInfoFromFile(af); err == nil {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b AgentInfo) int { return b.Updated.Compare(a.Updated) })
	return out, nil
}

// SetAgentName updates an agent's name and appends a name record to its live session.
func (h *Harness) SetAgentName(id, via, n string) error {
	if err := checkName(n); err != nil {
		return err
	}
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.a.Name = n
	if err := h.saveAgentFile(e.a); err != nil {
		return err
	}
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	_, err = h.SetName(cur, via, n)
	return err
}

// SetAgentConfig updates an agent's config lazily (Rule 1), applying SetWorkers
// in place when only workers changed and the live session is below limit.
func (h *Harness) SetAgentConfig(id, via string, cfg AgentConfig) error {
	if err := h.validateAgentConfig(&cfg); err != nil {
		return err
	}
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	if chain, err := h.Sessions.Load(cur); err == nil {
		if sConf, err := config(chain); err == nil {
			if sameCoreConfig(sConf, cfg) && !slices.Equal(sConf.Workers, cfg.Workers) && statefile.ContextUsed(chain) < cfg.ContextLimit {
				if _, err := h.SetWorkers(cur, via, cfg.Workers); err != nil {
					return err
				}
			}
		}
	}
	e.a.Config = cfg
	return h.saveAgentFile(e.a)
}

// SetAgentApps updates an agent's granted apps and appends an apps system note
// in place to its live session (Rule 1).
func (h *Harness) SetAgentApps(id, _ string, list []string) error {
	clean, err := cleanAppList(list)
	if err != nil {
		return err
	}
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.a.Apps = clean
	if err := h.saveAgentFile(e.a); err != nil {
		return err
	}
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	return h.note(h.session(cur), cur, system("apps", h.appsSummary(clean, true)))
}

// StopAgent stops an agent's live session.
func (h *Harness) StopAgent(ctx context.Context, id, via string) error {
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	e.mu.Unlock()
	return h.Stop(ctx, cur, via)
}

// RetryAgent retries an agent's live session.
func (h *Harness) RetryAgent(ctx context.Context, id string) error {
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	e.mu.Unlock()
	if _, err := h.Info(cur); err != nil {
		return err
	}
	h.Retry(ctx, cur)
	return nil
}

// DeleteAgent stops an agent's live session and deletes agents/<id>.json,
// leaving past session .state files intact.
func (h *Harness) DeleteAgent(ctx context.Context, id, via string) error {
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	_ = h.Stop(ctx, cur, via)
	s := h.session(cur)
	h.mu.Lock()
	if s.running {
		done := s.done
		h.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		h.mu.Unlock()
	}
	if err := os.Remove(h.agentPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	h.amu.Lock()
	delete(h.agentMap, id)
	delete(h.liveAgent, cur)
	for _, sid := range e.a.Sessions {
		delete(h.ownAgent, sid)
	}
	h.amu.Unlock()
	return nil
}

// SessionRecords reads a session's records after n, optionally waiting for new ones.
func (h *Harness) SessionRecords(ctx context.Context, sid string, after int, wait time.Duration) ([]statefile.Record, error) {
	changed := h.Changed(sid)
	recs, err := h.records(sid, after)
	if err != nil || len(recs) > 0 || wait <= 0 {
		return recs, err
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-changed:
	case <-t.C:
	case <-ctx.Done():
	}
	return h.records(sid, after)
}

func (h *Harness) shouldIdleRollover(chain []statefile.Record, cfg AgentConfig) bool {
	if len(chain) < 2 {
		return false
	}
	var rec2 statefile.Conf
	if json.Unmarshal(chain[1].Body, &rec2) != nil {
		return false
	}
	if !sameCoreConfig(rec2, cfg) {
		return true
	}
	used := statefile.ContextUsed(chain)
	if used >= cfg.ContextLimit {
		return true
	}
	if statefile.LastStep(chain) != nil && cfg.ColdLimit > 0 && used >= cfg.ColdLimit {
		var cache Cache
		for _, r := range chain {
			if c, ok := cacheOf(r); ok {
				cache = c
			}
		}
		if cache.Until.IsZero() || !time.Now().Before(cache.Until) {
			return true
		}
	}
	if p, model, err := h.provider(rec2.Model); err == nil && rebuilt(p, model, rec2, chain) != "" {
		return true
	}
	return false
}

// PostAgent appends parts to an agent, rolling over first if Rule 2 triggers.
func (h *Harness) PostAgent(ctx context.Context, id, via, key string, parts []Part) (string, int, error) {
	e, err := h.lookupAgentEntry(id)
	if err != nil {
		return "", 0, err
	}
	e.mu.Lock()
	cur := e.a.Sessions[len(e.a.Sessions)-1]
	s := h.session(cur)
	h.mu.Lock()
	running := s.running
	h.mu.Unlock()
	if !running {
		chain, err := h.Sessions.Load(cur)
		if err != nil {
			e.mu.Unlock()
			return "", 0, err
		}
		if h.shouldIdleRollover(chain, e.a.Config) {
			h.copyLogsForce(ctx, s)
			nextID := statefile.NewID()
			if err := h.createAgentSession(ctx, nextID, e.a.ID, e.a.Name, e.a.Config, e.a.Apps, cur, e.a.Sessions); err != nil {
				e.mu.Unlock()
				return "", 0, err
			}
			if err := h.note(s, cur, system("continue", nextID)); err != nil {
				e.mu.Unlock()
				return "", 0, err
			}
			e.a.Sessions = append(e.a.Sessions, nextID)
			if err := h.saveAgentFile(e.a); err != nil {
				e.mu.Unlock()
				return "", 0, err
			}
			h.amu.Lock()
			delete(h.liveAgent, cur)
			h.liveAgent[nextID] = e.a.ID
			h.ownAgent[nextID] = e.a.ID
			h.amu.Unlock()
			cur = nextID
		}
	}
	e.mu.Unlock()
	n, err := h.Post(ctx, cur, via, key, parts)
	return cur, n, err
}

// PostText posts a single text/plain message to an agent using the harness's
// background lifetime context so returning from an app HTTP handler does not
// cancel the kicked step.
func (h *Harness) PostText(ctx context.Context, id, via, text string) (string, int, error) {
	return h.PostAgent(h.stepCtx(ctx), id, via, "", []Part{{Type: "text/plain", Body: []byte(text)}})
}

// checkMidRunRollover checks whether a stepping live agent session crossed its
// context limit or had its core config changed mid-run (Rule 3).
func (h *Harness) checkMidRunRollover(id string, conf statefile.Conf, chain []statefile.Record) (nextID, reason string) {
	e, ok := h.liveEntryForSession(id)
	if !ok {
		return "", ""
	}
	e.mu.Lock()
	af := e.a
	e.mu.Unlock()
	if len(af.Sessions) == 0 || af.Sessions[len(af.Sessions)-1] != id {
		return "", ""
	}
	p := statefile.ContextUsed(chain)
	limit := min(conf.ContextLimit, af.Config.ContextLimit)
	switch {
	case p > limit:
		return statefile.NewID(), fmt.Sprintf("context %d exceeds limit %d", p, limit)
	case !sameCoreConfig(conf, af.Config):
		return statefile.NewID(), "agent config changed mid-run"
	default:
		return "", ""
	}
}

// finishMidRunRollover runs after copyLogsForce(s) completes on workers,
// creating nextID, revoking cur's session key, moving unaddressed mid-step
// messages to nextID, and kicking nextID.
func (h *Harness) finishMidRunRollover(ctx context.Context, cur, nextID string, s *session) {
	e, ok := h.liveEntryForSession(cur)
	if !ok {
		h.mu.Lock()
		s.running, s.cancel, s.kicked = false, nil, false
		h.mu.Unlock()
		return
	}
	e.mu.Lock()
	if len(e.a.Sessions) == 0 || e.a.Sessions[len(e.a.Sessions)-1] != cur {
		e.mu.Unlock()
		h.mu.Lock()
		s.running, s.cancel, s.kicked = false, nil, false
		h.mu.Unlock()
		return
	}
	if err := h.createAgentSession(ctx, nextID, e.a.ID, e.a.Name, e.a.Config, e.a.Apps, cur, e.a.Sessions); err != nil {
		e.mu.Unlock()
		h.mu.Lock()
		s.running, s.cancel, s.kicked = false, nil, false
		h.mu.Unlock()
		return
	}
	// Transition s out of running while holding e.mu so new PostAgent calls
	// wait for e.mu and land cleanly on nextID.
	h.mu.Lock()
	s.running, s.cancel, s.kicked = false, nil, false
	h.mu.Unlock()

	s.mu.Lock()
	chain, loadErr := h.Sessions.Load(cur)
	s.mu.Unlock()

	var carry []statefile.Record
	if loadErr == nil {
		upto := 0
		if last := statefile.LastStep(chain); last != nil {
			upto = last.Upto
		}
		for _, r := range chain {
			if r.Kind == statefile.Message && r.N > upto {
				carry = append(carry, r)
			}
		}
	}

	e.a.Sessions = append(e.a.Sessions, nextID)
	_ = h.saveAgentFile(e.a)
	h.amu.Lock()
	delete(h.liveAgent, cur)
	h.liveAgent[nextID] = e.a.ID
	h.ownAgent[nextID] = e.a.ID
	h.amu.Unlock()
	e.mu.Unlock()

	ns := h.session(nextID)
	for _, r := range carry {
		_ = h.note(ns, nextID, statefile.Record{
			Header: statefile.Header{Kind: statefile.Message, Type: r.Type, Via: r.Via, IdempotencyKey: r.IdempotencyKey},
			Body:   r.Body,
		})
	}
	contMsg := fmt.Sprintf("Session %s hit its context limit mid-task; retrieve from <scratch>/logs/%s.log and continue", cur, cur)
	_, _ = h.Post(ctx, nextID, "continue", "", []Part{{Type: "text/plain", Body: []byte(contMsg)}})
}

// registerAgentRoutes mounts the 10 /agents HTTP routes on mux.
func (h *Harness) registerAgentRoutes(ctx context.Context, mux *http.ServeMux) {
	h.amu.Lock()
	h.runCtx = ctx
	h.amu.Unlock()
	handleAgent := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if id := r.PathValue("id"); !statefile.ValidID(id) {
				fail(w, fmt.Errorf("bad agent id %q", id))
				return
			}
			fn(w, r)
		})
	}
	mux.HandleFunc("GET /agents", h.serveAgentsList)
	mux.HandleFunc("POST /agents", func(w http.ResponseWriter, r *http.Request) {
		var c AgentCreate
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxPost)).Decode(&c); err != nil {
			fail(w, err)
			return
		}
		id, sid, err := h.CreateAgent(ctx, c, r.URL.Query().Get("via"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]string{"id": id, "session": sid})
	})
	handleAgent("GET /agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		info, err := h.GetAgent(r.PathValue("id"))
		if writeAgentFail(w, err) {
			return
		}
		reply(w, info)
	})
	handleAgent("DELETE /agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := via(r)
		if err != nil {
			fail(w, err)
			return
		}
		if err := h.DeleteAgent(r.Context(), r.PathValue("id"), v); writeAgentFail(w, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handleAgent("POST /agents/{id}/records", func(w http.ResponseWriter, r *http.Request) {
		h.serveAgentPost(ctx, w, r)
	})
	handleAgent("POST /agents/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		v, err := via(r)
		if err != nil {
			fail(w, err)
			return
		}
		if err := h.StopAgent(ctx, r.PathValue("id"), v); writeAgentFail(w, err) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handleAgent("POST /agents/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if err := h.RetryAgent(ctx, r.PathValue("id")); writeAgentFail(w, err) {
			return
		}
		reply(w, map[string]statefile.Status{"status": statefile.StatusStepping})
	})
	handleAgent("PUT /agents/{id}/name", h.serveAgentName)
	handleAgent("PUT /agents/{id}/config", h.serveAgentConfig)
	handleAgent("PUT /agents/{id}/apps", h.serveAgentApps)
}

func (h *Harness) serveAgentName(w http.ResponseWriter, r *http.Request) {
	v, err := via(r)
	if err != nil {
		fail(w, err)
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxName+1))
	if err != nil {
		fail(w, err)
		return
	}
	if err := h.SetAgentName(r.PathValue("id"), v, string(b)); writeAgentFail(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Harness) serveAgentConfig(w http.ResponseWriter, r *http.Request) {
	v, err := via(r)
	if err != nil {
		fail(w, err)
		return
	}
	var cfg AgentConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxPost)).Decode(&cfg); err != nil {
		fail(w, err)
		return
	}
	if err := h.SetAgentConfig(r.PathValue("id"), v, cfg); writeAgentFail(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Harness) serveAgentApps(w http.ResponseWriter, r *http.Request) {
	v, err := via(r)
	if err != nil {
		fail(w, err)
		return
	}
	var list []string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxPost)).Decode(&list); err != nil {
		fail(w, err)
		return
	}
	if err := h.SetAgentApps(r.PathValue("id"), v, list); writeAgentFail(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Harness) serveAgentsList(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	if caller.Kind == auth.App {
		if h.Apps == nil || !h.Apps.HasEntitlement(caller.App, "list") {
			http.Error(w, "app lacks list entitlement", http.StatusForbidden)
			return
		}
		infos, err := h.ListAgents()
		if err != nil {
			fail(w, err)
			return
		}
		type brief struct {
			ID   string `json:"id"`
			Name string `json:"name,omitempty"`
		}
		out := []brief{}
		for _, in := range infos {
			if slices.Contains(in.Apps, caller.App) {
				out = append(out, brief{ID: in.ID, Name: in.Name})
			}
		}
		reply(w, out)
		return
	}
	infos, err := h.ListAgents()
	if err != nil {
		fail(w, err)
		return
	}
	q := r.URL.Query()
	if v := q.Get("via"); v != "" {
		infos = slices.DeleteFunc(infos, func(i AgentInfo) bool { return i.Via != v })
	}
	if st := q.Get("status"); st != "" {
		infos = slices.DeleteFunc(infos, func(i AgentInfo) bool { return string(i.Status) != st })
	}
	if after := q.Get("after"); after != "" {
		if idx := slices.IndexFunc(infos, func(i AgentInfo) bool { return i.ID == after }); idx >= 0 {
			infos = infos[idx+1:]
		}
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n >= 0 && n < len(infos) {
		infos = infos[:n]
	}
	reply(w, infos)
}

func (h *Harness) serveAgentPost(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	caller := auth.CallerFrom(r.Context())
	var v string
	if caller.Kind == auth.App {
		if h.Apps == nil || !h.Apps.HasEntitlement(caller.App, "post") {
			http.Error(w, "app lacks post entitlement", http.StatusForbidden)
			return
		}
		if !h.AgentHasApp(id, caller.App) {
			http.Error(w, "target agent does not grant this app", http.StatusForbidden)
			return
		}
		v = "app:" + caller.App
	} else {
		var err error
		if v, err = via(r); err != nil {
			fail(w, err)
			return
		}
	}
	p, err := parts(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, MaxPost))
	if err != nil {
		fail(w, err)
		return
	}
	sid, n, err := h.PostAgent(ctx, id, v, r.Header.Get("Idempotency-Key"), p)
	if writeAgentFail(w, err) {
		return
	}
	reply(w, map[string]any{"session": sid, "n": n})
}

func writeAgentFail(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return true
	}
	fail(w, err)
	return true
}
