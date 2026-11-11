// Package orchestrate is the built-in subset-scoped agent management app.
package orchestrate

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// maxBody bounds an orchestrate request payload.
const maxBody = 1 << 20

//go:embed skill.md
var Skill string

//go:embed app.json
var ManifestJSON []byte

// AgentConfig is the session configuration template an agent holds for its
// next session, plus ColdLimit for cache-aware idle rollover.
type AgentConfig struct {
	Model        string                     `json:"model"`
	Tools        string                     `json:"tools"`
	Workers      []statefile.Worker         `json:"workers"`
	ContextLimit int                        `json:"context_limit"`
	ColdLimit    int                        `json:"cold_limit,omitempty"`
	Input        []string                   `json:"input,omitempty"`
	Params       map[string]json.RawMessage `json:"params,omitempty"`
	Preset       string                     `json:"preset,omitempty"`
}

// AgentInfo is what GET /agents and GET /agents/{id} report about an agent.
type AgentInfo struct {
	ID             string           `json:"id"`
	Name           string           `json:"name,omitempty"`
	Via            string           `json:"via,omitempty"`
	Created        time.Time        `json:"created"`
	Updated        time.Time        `json:"updated"`
	Status         statefile.Status `json:"status"`
	Config         AgentConfig      `json:"config"`
	Apps           []string         `json:"apps"`
	Sessions       []string         `json:"sessions"`
	CurrentSession string           `json:"current_session"`
	ContextUsed    int              `json:"context_used"`
	Usage          statefile.Usage  `json:"usage"`
}

// AgentCreate is the body of POST /agents.
type AgentCreate struct {
	Name    string       `json:"name,omitempty"`
	Config  *AgentConfig `json:"config,omitempty"`
	Apps    []string     `json:"apps,omitempty"`
	Session string       `json:"session,omitempty"`
}

// Host is the harness surface orchestrate calls.
type Host interface {
	GetAgent(id string) (AgentInfo, error)
	SessionAgent(sessionID string) (AgentInfo, bool)
	ListAgents() ([]AgentInfo, error)
	CreateAgent(ctx context.Context, c AgentCreate, via string) (id, session string, err error)
	PostText(ctx context.Context, id, via, text string) (session string, n int, err error)
	SetAgentName(id, via, name string) error
	SetAgentConfig(id, via string, cfg AgentConfig) error
	SetAgentApps(id, via string, apps []string) error
	StopAgent(ctx context.Context, id, via string) error
	DeleteAgent(ctx context.Context, id, via string) error
	SessionRecords(ctx context.Context, sid string, after int, wait time.Duration) ([]statefile.Record, error)
}

// App implements subset-scoped /agents operations and opt-in notify_on_stop.
type App struct {
	path    string
	host    Host
	mu      sync.Mutex
	watches map[string][]string // target agent ID -> watcher agent IDs
}

func New(dataDir string, host Host) (*App, error) {
	a := &App{
		path:    filepath.Join(dataDir, "watches.json"),
		host:    host,
		watches: map[string][]string{},
	}
	b, err := os.ReadFile(a.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &a.watches); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *App) saveLocked() error {
	b, err := json.MarshalIndent(a.watches, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *App) addWatch(target, watcher string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if slices.Contains(a.watches[target], watcher) {
		return nil
	}
	prev := slices.Clone(a.watches[target])
	a.watches[target] = append(slices.Clone(prev), watcher)
	if err := a.saveLocked(); err != nil {
		if len(prev) == 0 {
			delete(a.watches, target)
		} else {
			a.watches[target] = prev
		}
		return err
	}
	return nil
}

func (a *App) removeWatch(target, watcher string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	prev := slices.Clone(a.watches[target])
	list := slices.DeleteFunc(slices.Clone(prev), func(w string) bool { return w == watcher })
	if len(list) == len(prev) {
		return nil
	}
	if len(list) == 0 {
		delete(a.watches, target)
	} else {
		a.watches[target] = list
	}
	if err := a.saveLocked(); err != nil {
		a.watches[target] = prev
		return err
	}
	return nil
}

func (a *App) clearWatch(target string) {
	a.mu.Lock()
	if _, ok := a.watches[target]; ok {
		delete(a.watches, target)
		_ = a.saveLocked()
	}
	a.mu.Unlock()
}

// OnAgentStop fires one notice per subscribed watcher when target stops terminally.
func (a *App) OnAgentStop(ctx context.Context, target AgentInfo) {
	a.mu.Lock()
	watchers := a.watches[target.ID]
	if len(watchers) > 0 {
		delete(a.watches, target.ID)
		if err := a.saveLocked(); err != nil {
			log.Printf("orchestrate: save watches: %v", err)
		}
	}
	a.mu.Unlock()

	label := target.ID
	if target.Name != "" {
		label = fmt.Sprintf("%s (%s)", target.ID, target.Name)
	}
	msg := fmt.Sprintf("Agent %s stopped with status %s (session %s).", label, target.Status, target.CurrentSession)
	for _, w := range watchers {
		if _, _, err := a.host.PostText(ctx, w, "app:orchestrate", msg); err != nil {
			log.Printf("orchestrate: notify %s of %s: %v", w, target.ID, err)
		}
	}
}

func workersSubset(target, caller []statefile.Worker) bool {
	for _, tw := range target {
		if !slices.ContainsFunc(caller, func(cw statefile.Worker) bool { return cw.Name == tw.Name }) {
			return false
		}
	}
	return true
}

func appsSubset(target, caller []string) bool {
	for _, ta := range target {
		if !slices.Contains(caller, ta) {
			return false
		}
	}
	return true
}

func allowedTarget(target, caller AgentInfo) bool {
	return workersSubset(target.Config.Workers, caller.Config.Workers) && appsSubset(target.Apps, caller.Apps)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	callerID := r.Header.Get("X-Mainplane-Agent")
	if callerID == "" {
		http.Error(w, "agent context required", http.StatusForbidden)
		return
	}
	caller, err := a.host.GetAgent(callerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	p := strings.Trim(r.URL.Path, "/")
	verb, rest, _ := strings.Cut(p, "/")
	switch {
	case r.Method == http.MethodPost && verb == "spawn" && rest == "":
		a.handleSpawn(w, r, caller)
	case r.Method == http.MethodGet && (p == "" || p == "list" || p == "agents"):
		a.handleList(w, r, caller)
	case r.Method == http.MethodGet && (verb == "get" || verb == "agents") && rest != "" && !strings.Contains(rest, "/"):
		a.handleGet(w, caller, rest)
	case r.Method == http.MethodGet && verb == "records" && rest != "":
		a.handleRecords(w, r, caller, rest)
	case (r.Method == http.MethodPost || r.Method == http.MethodPut) && verb == "update" && rest != "":
		a.handleUpdate(w, r, caller, rest)
	case r.Method == http.MethodPut && verb == "agents" && rest != "" && !strings.Contains(rest, "/"):
		a.handleUpdate(w, r, caller, rest)
	case r.Method == http.MethodPost && verb == "watch" && rest != "":
		a.handleWatch(w, caller, rest)
	case r.Method == http.MethodPost && verb == "stop" && rest != "":
		a.handleStop(w, r, caller, rest)
	case r.Method == http.MethodPost && verb == "agents" && strings.HasSuffix(rest, "/stop"):
		a.handleStop(w, r, caller, strings.TrimSuffix(rest, "/stop"))
	case r.Method == http.MethodDelete && verb != "" && rest == "":
		a.handleDelete(w, r, caller, verb)
	case (r.Method == http.MethodPost && verb == "delete" || r.Method == http.MethodDelete && verb == "agents") && rest != "":
		a.handleDelete(w, r, caller, rest)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleSpawn(w http.ResponseWriter, r *http.Request, caller AgentInfo) {
	var in struct {
		Name         string       `json:"name"`
		Config       *AgentConfig `json:"config"`
		Apps         []string     `json:"apps"`
		Text         string       `json:"text"`
		NotifyOnStop bool         `json:"notify_on_stop"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := caller.Config
	if in.Config != nil {
		if in.Config.Workers != nil && !workersSubset(in.Config.Workers, caller.Config.Workers) {
			http.Error(w, "target workers exceed caller workers", http.StatusForbidden)
			return
		}
		cfg = mergeConfig(caller.Config, *in.Config)
	}
	if !appsSubset(in.Apps, caller.Apps) {
		http.Error(w, "target apps exceed caller apps", http.StatusForbidden)
		return
	}
	via := "agent:" + caller.ID
	id, sid, err := a.host.CreateAgent(r.Context(), AgentCreate{Name: in.Name, Config: &cfg, Apps: in.Apps}, via)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.NotifyOnStop {
		if err := a.addWatch(id, caller.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if in.Text != "" {
		if sid, _, err = a.host.PostText(r.Context(), id, via, in.Text); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	replyJSON(w, map[string]string{"id": id, "session": sid})
}

func mergeConfig(base, over AgentConfig) AgentConfig {
	out := over
	if out.Model == "" {
		out.Model = base.Model
	}
	if out.Tools == "" {
		out.Tools = base.Tools
	}
	if out.ContextLimit == 0 {
		out.ContextLimit = base.ContextLimit
	}
	if out.Workers == nil {
		out.Workers = slices.Clone(base.Workers)
	}
	return out
}

func (a *App) handleList(w http.ResponseWriter, r *http.Request, caller AgentInfo) {
	infos, err := a.host.ListAgents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	infos = slices.DeleteFunc(infos, func(i AgentInfo) bool { return !allowedTarget(i, caller) })
	q := r.URL.Query()
	if v := q.Get("via"); v != "" {
		infos = slices.DeleteFunc(infos, func(i AgentInfo) bool { return i.Via != v })
	}
	if st := q.Get("status"); st != "" {
		infos = slices.DeleteFunc(infos, func(i AgentInfo) bool { return string(i.Status) != st })
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n >= 0 && n < len(infos) {
		infos = infos[:n]
	}
	replyJSON(w, infos)
}

func (a *App) handleGet(w http.ResponseWriter, caller AgentInfo, id string) {
	info, err := a.host.GetAgent(id)
	if writeAgentErr(w, err) {
		return
	}
	if !allowedTarget(info, caller) {
		http.Error(w, "target permissions exceed caller", http.StatusForbidden)
		return
	}
	replyJSON(w, info)
}

func (a *App) handleRecords(w http.ResponseWriter, r *http.Request, caller AgentInfo, sid string) {
	if !statefile.ValidID(sid) {
		http.Error(w, "bad session id", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	after, _ := strconv.Atoi(q.Get("after"))
	wait, _ := time.ParseDuration(q.Get("wait"))
	head, err := a.host.SessionRecords(r.Context(), sid, 0, 0)
	if writeAgentErr(w, err) {
		return
	}
	owner, ok := a.host.SessionAgent(sid)
	if !ok || !allowedTarget(owner, caller) {
		http.Error(w, "session permissions exceed caller", http.StatusForbidden)
		return
	}
	for _, rec := range head {
		if rec.Kind == statefile.Config {
			var conf statefile.Conf
			if json.Unmarshal(rec.Body, &conf) == nil && !workersSubset(conf.Workers, caller.Config.Workers) {
				http.Error(w, "session workers exceed caller workers", http.StatusForbidden)
				return
			}
		}
	}
	recs, err := a.host.SessionRecords(r.Context(), sid, after, wait)
	if writeAgentErr(w, err) {
		return
	}
	if q.Get("format") == "json" {
		type item struct {
			statefile.Header
			Body string `json:"body,omitempty"`
		}
		out := make([]item, 0, len(recs))
		for _, rec := range recs {
			out = append(out, item{Header: rec.Header, Body: string(rec.Body)})
		}
		replyJSON(w, out)
		return
	}
	w.Header().Set("Content-Type", "application/x-mainplane-records")
	for _, rec := range recs {
		b, err := statefile.Encode(rec)
		if err != nil {
			return
		}
		_, _ = w.Write(b)
	}
}

func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request, caller AgentInfo, id string) {
	target, err := a.host.GetAgent(id)
	if writeAgentErr(w, err) {
		return
	}
	if !allowedTarget(target, caller) {
		http.Error(w, "target permissions exceed caller", http.StatusForbidden)
		return
	}
	var in struct {
		Name         *string      `json:"name"`
		Config       *AgentConfig `json:"config"`
		Apps         *[]string    `json:"apps"`
		NotifyOnStop *bool        `json:"notify_on_stop"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.Config != nil && !workersSubset(in.Config.Workers, caller.Config.Workers) {
		http.Error(w, "target workers exceed caller workers", http.StatusForbidden)
		return
	}
	if in.Apps != nil && !appsSubset(*in.Apps, caller.Apps) {
		http.Error(w, "target apps exceed caller apps", http.StatusForbidden)
		return
	}
	via := "agent:" + caller.ID
	if in.Config != nil {
		cfg := mergeConfig(target.Config, *in.Config)
		if err := a.host.SetAgentConfig(id, via, cfg); writeAgentErr(w, err) {
			return
		}
	}
	if in.Apps != nil {
		if err := a.host.SetAgentApps(id, via, *in.Apps); writeAgentErr(w, err) {
			return
		}
	}
	if in.Name != nil {
		if err := a.host.SetAgentName(id, via, *in.Name); writeAgentErr(w, err) {
			return
		}
	}
	if in.NotifyOnStop != nil {
		var werr error
		if *in.NotifyOnStop {
			werr = a.addWatch(id, caller.ID)
		} else {
			werr = a.removeWatch(id, caller.ID)
		}
		if writeAgentErr(w, werr) {
			return
		}
	}
	replyJSON(w, map[string]string{"id": id})
}

func (a *App) handleWatch(w http.ResponseWriter, caller AgentInfo, id string) {
	target, err := a.host.GetAgent(id)
	if writeAgentErr(w, err) {
		return
	}
	if !allowedTarget(target, caller) {
		http.Error(w, "target permissions exceed caller", http.StatusForbidden)
		return
	}
	if err := a.addWatch(id, caller.ID); writeAgentErr(w, err) {
		return
	}
	replyJSON(w, map[string]string{"id": id})
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request, caller AgentInfo, id string) {
	target, err := a.host.GetAgent(id)
	if writeAgentErr(w, err) {
		return
	}
	if !allowedTarget(target, caller) {
		http.Error(w, "target permissions exceed caller", http.StatusForbidden)
		return
	}
	if err := a.host.StopAgent(r.Context(), id, "agent:"+caller.ID); writeAgentErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request, caller AgentInfo, id string) {
	target, err := a.host.GetAgent(id)
	if writeAgentErr(w, err) {
		return
	}
	if !allowedTarget(target, caller) {
		http.Error(w, "target permissions exceed caller", http.StatusForbidden)
		return
	}
	if err := a.host.DeleteAgent(r.Context(), id, "agent:"+caller.ID); writeAgentErr(w, err) {
		return
	}
	a.clearWatch(id)
	w.WriteHeader(http.StatusNoContent)
}

func writeAgentErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return true
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
	return true
}

func replyJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
