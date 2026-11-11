// Package orchestrate is the orchestrate app: an agent runs agents of its
// own from a worker, with no api key. Each one it spawns has via
// agent:<caller>, which is what makes it the caller's to see and change, and
// never more workers or apps than the caller has. It is silent: a caller
// hears of a stop only when it asked to.
package orchestrate

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

//go:embed skill.md
var Skill []byte

// Name is the app's, and its notices come via app:<Name>.
const Name = "orchestrate"

type orchestrate struct {
	ctx  context.Context
	h    *harness.Harness
	path string

	mu    sync.Mutex
	watch map[string]string // spawned agent -> the caller to tell when it stops
}

// verb is one route; a nil reply is 204 and written means it wrote its own.
type verb func(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error)

// New serves the verbs and posts one notice per watch under ctx.
func New(ctx context.Context, h *harness.Harness, dir string) (http.Handler, error) {
	o := &orchestrate{ctx: ctx, h: h, path: filepath.Join(dir, "watch.json"), watch: map[string]string{}}
	b, err := os.ReadFile(o.path)
	if err == nil {
		err = json.Unmarshal(b, &o.watch)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	h.OnStop(o.stopped)
	mux := http.NewServeMux()
	for pattern, fn := range map[string]verb{
		"POST /spawn":              o.spawn,
		"GET /agents":              o.list,
		"GET /agents/{id}":         o.get,
		"GET /agents/{id}/records": o.records,
		"PUT /agents/{id}":         o.update,
		"POST /agents/{id}/watch":  o.setWatchOf,
		"POST /agents/{id}/stop":   o.stop,
		"DELETE /agents/{id}":      o.delete,
	} {
		mux.HandleFunc(pattern, o.serve(fn))
	}
	return mux, nil
}

// serve runs fn for the calling agent, on an agent it spawned when the path
// names one.
func (o *orchestrate) serve(fn verb) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, err := o.h.GetAgent(r.Header.Get(apps.AgentHeader))
		if err == nil && r.PathValue("id") != "" {
			err = o.mine(caller, r.PathValue("id"))
		}
		var v any
		if err == nil {
			v, err = fn(w, r, caller)
		}
		var code *status
		switch {
		case errors.As(err, &code):
			http.Error(w, err.Error(), code.code)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		case v == nil:
			w.WriteHeader(http.StatusNoContent)
		case v != written:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
}

func via(caller harness.Agent) string { return "agent:" + caller.ID }

func (o *orchestrate) spawn(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	var in struct {
		harness.NewAgent
		Task         string `json:"task"`
		NotifyOnStop bool   `json:"notify_on_stop"`
	}
	if err := decode(w, r, &in); err != nil {
		return nil, err
	}
	if in.Session != "" {
		return nil, errors.New("spawn makes a new session; it adopts none")
	}
	if in.Config == nil {
		in.Config = &caller.Config
	}
	if err := within(caller, *in.Config, in.Apps); err != nil {
		return nil, err
	}
	a, err := o.h.CreateAgent(o.ctx, in.NewAgent, via(caller))
	if err != nil {
		return nil, err
	}
	if in.NotifyOnStop {
		err = o.setWatch(a.ID, caller.ID, true)
	}
	if err == nil && in.Task != "" {
		_, _, err = o.h.PostAgent(o.ctx, a.ID, via(caller), "", []harness.Part{{Type: "text/plain", Body: []byte(in.Task)}})
	}
	if err != nil { // a spawn is whole or not at all
		if derr := errors.Join(o.h.DeleteAgent(o.ctx, a.ID, via(caller)), o.setWatch(a.ID, caller.ID, false)); derr != nil {
			log.Printf("orchestrate: undo of the spawn of %s: %v", a.ID, derr)
		}
		return nil, err
	}
	return map[string]string{"id": a.ID, "session": a.Live()}, nil
}

func (o *orchestrate) list(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	return slices.DeleteFunc(o.h.AgentInfos(), func(i harness.AgentInfo) bool { return i.Via != via(caller) }), nil
}

func (o *orchestrate) get(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	return o.h.AgentInfo(r.PathValue("id"))
}

// records are those of the agent's live session, or of ?session= of its own.
func (o *orchestrate) records(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	a, err := o.h.GetAgent(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	sid := r.URL.Query().Get("session")
	if sid == "" {
		sid = a.Live()
	}
	if !slices.Contains(a.Sessions, sid) {
		return nil, fmt.Errorf("session %s is not agent %s's", sid, a.ID)
	}
	o.h.ServeRecords(w, r, sid)
	return written, nil
}

func (o *orchestrate) update(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	var in struct {
		Name   *string              `json:"name"`
		Config *harness.AgentConfig `json:"config"`
		Apps   *[]string            `json:"apps"`
	}
	if err := decode(w, r, &in); err != nil {
		return nil, err
	}
	a, err := o.h.GetAgent(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if in.Config != nil {
		a.Config = *in.Config
	}
	if in.Apps != nil {
		a.Apps = *in.Apps
	}
	if err := within(caller, a.Config, a.Apps); err != nil {
		return nil, err
	}
	if in.Name != nil {
		err = o.h.SetAgentName(a.ID, *in.Name)
	}
	if err == nil && in.Config != nil {
		err = o.h.SetAgentConfig(a.ID, via(caller), a.Config)
	}
	if err == nil && in.Apps != nil {
		err = o.h.SetAgentApps(a.ID, a.Apps)
	}
	return nil, err
}

func (o *orchestrate) setWatchOf(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	var in struct {
		NotifyOnStop bool `json:"notify_on_stop"`
	}
	if err := decode(w, r, &in); err != nil {
		return nil, err
	}
	return nil, o.setWatch(r.PathValue("id"), caller.ID, in.NotifyOnStop)
}

func (o *orchestrate) stop(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	return nil, o.h.StopAgent(o.ctx, r.PathValue("id"), via(caller))
}

func (o *orchestrate) delete(w http.ResponseWriter, r *http.Request, caller harness.Agent) (any, error) {
	if err := o.setWatch(r.PathValue("id"), caller.ID, false); err != nil {
		return nil, err
	}
	return nil, o.h.DeleteAgent(o.ctx, r.PathValue("id"), via(caller))
}

// written is a handler's word that it wrote the reply itself.
var written = new(int)

// status is an error with its HTTP status.
type status struct {
	code int
	msg  string
}

func (s *status) Error() string { return s.msg }

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(v)
}

// mine is nil when caller spawned agent id.
func (o *orchestrate) mine(caller harness.Agent, id string) error {
	if a, err := o.h.GetAgent(id); err != nil || a.Via != "agent:"+caller.ID {
		return &status{http.StatusNotFound, fmt.Sprintf("no agent %s spawned by agent %s", id, caller.ID)}
	}
	return nil
}

// within is nil when an agent of config c and apps has no worker and no app
// the caller lacks.
func within(caller harness.Agent, c harness.AgentConfig, names []string) error {
	for _, w := range c.Workers {
		if !slices.ContainsFunc(caller.Config.Workers, func(x statefile.Worker) bool { return x.Name == w.Name }) {
			return &status{http.StatusForbidden, fmt.Sprintf("worker %s is not one of agent %s's workers", w.Name, caller.ID)}
		}
	}
	for _, n := range names {
		if !slices.Contains(caller.Apps, n) {
			return &status{http.StatusForbidden, fmt.Sprintf("app %s is not one of agent %s's apps", n, caller.ID)}
		}
	}
	return nil
}

// setWatch asks for a notice to parent when child next stops, or takes the
// ask back; watch.json keeps it over a restart.
func (o *orchestrate) setWatch(child, parent string, on bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if on {
		o.watch[child] = parent
	} else {
		delete(o.watch, child)
	}
	return o.save()
}

// save writes watch.json through a file beside it, under mu.
func (o *orchestrate) save() error {
	b, err := json.MarshalIndent(o.watch, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.path+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(o.path+".tmp", o.path)
}

// stopped posts the one notice a watch asked for.
func (o *orchestrate) stopped(agent string, st statefile.Status) {
	o.mu.Lock()
	parent, ok := o.watch[agent]
	if ok {
		delete(o.watch, agent)
		if err := o.save(); err != nil {
			log.Printf("orchestrate: %v", err)
		}
	}
	o.mu.Unlock()
	if !ok {
		return
	}
	text := fmt.Sprintf("Agent %s stopped: its session is %s. Its records: GET $MAINPLANE_URL/apps/orchestrate/agents/%s/records.", agent, st, agent)
	if _, _, err := o.h.PostAgent(o.ctx, parent, "app:"+Name, "", []harness.Part{{Type: "text/plain", Body: []byte(text)}}); err != nil {
		log.Printf("orchestrate: notice of %s to %s: %v", agent, parent, err)
	}
}
