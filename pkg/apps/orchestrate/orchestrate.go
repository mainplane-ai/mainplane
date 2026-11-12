// Package orchestrate is the built-in app that gives an agent the /agents
// routes for agents of its own: it creates them, reads them and stops them,
// and none of them may reach a worker or an app that it cannot.
package orchestrate

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

//go:embed skill.md
var Skill string

// stopped is what a watching agent is told, once.
const stopped = "Agent %s%s, which you created, stopped: its session %s is %s."

// App is the handler and the watches: which agent to tell when one it
// created stops.
type App struct {
	http.Handler
	ctx  context.Context
	h    *harness.Harness
	path string

	mu      sync.Mutex
	watches map[string]string // created agent -> the agent that asked to be told
}

// create is the body of POST /agents: the harness's, and what only
// orchestrate takes.
type create struct {
	harness.AgentCreate
	Message      string `json:"message"`
	NotifyOnStop bool   `json:"notify_on_stop"`
}

// errEscalate is a create or change that asks for more than the caller has.
var errEscalate = errors.New("an agent you create may use only workers and apps you have")

// New loads the watches in dir and is the app: the harness's /agents routes
// for the agents the caller created, each answered by the harness's own
// route once the agent is seen to be the caller's. The caller is the via.
//
//	POST   /agents                      AgentCreate body, no "session"; and        -> {"id","session"}
//	                                    "message", "notify_on_stop". With no
//	                                    "config" the agent gets the caller's
//	GET    /agents                      ?status= ?limit= ?after=<id>               -> [AgentInfo]
//	GET    /agents/{id}                 -> AgentInfo
//	GET    /agents/{id}/records         ?session=, else the live one; then as      -> records
//	                                    GET /sessions/{id}/records: ?after=N ?wait=
//	PUT    /agents/{id}/name            the name as text                           -> 204
//	PUT    /agents/{id}/config          AgentConfig                                -> 204
//	PUT    /agents/{id}/apps            ["app"]                                    -> 204
//	PUT    /agents/{id}/notify_on_stop  true or false                              -> 204
//	POST   /agents/{id}/stop            -> 204
//	DELETE /agents/{id}                 -> 204
func New(ctx context.Context, h *harness.Harness, dir string) (*App, error) {
	o := &App{ctx: ctx, h: h, path: filepath.Join(dir, "watches.json"), watches: map[string]string{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(o.path)
	if err == nil {
		err = json.Unmarshal(b, &o.watches)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("orchestrate: %s: %w", o.path, err)
	}
	api, mux := harness.Handler(ctx, h), http.NewServeMux()
	// handle answers fn's error: 403 for more than the caller has, 404 for
	// an agent that is not the caller's, as if it were not there.
	handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			caller, err := h.Agent(r.Header.Get("X-Mainplane-Agent"))
			if err == nil {
				r.Body = http.MaxBytesReader(w, r.Body, harness.MaxPost)
				err = fn(w, r, caller)
			}
			switch {
			case errors.Is(err, errEscalate):
				http.Error(w, err.Error(), http.StatusForbidden)
			case errors.Is(err, harness.ErrNoAgent):
				http.Error(w, err.Error(), http.StatusNotFound)
			case err != nil:
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
		})
	}
	// pass gives the request to the harness's route of the same path, with
	// the caller as its via. On a list, via is the filter: the caller's own.
	pass := func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
		q := r.URL.Query()
		q.Set("via", harness.AgentVia+caller.ID)
		r.URL.RawQuery = q.Encode()
		api.ServeHTTP(w, r)
		return nil
	}
	// owned is pass for an agent the caller created, once check, if any,
	// allows the request.
	owned := func(pattern string, check func(r *http.Request, caller, a harness.Agent) error) {
		handle(pattern, func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
			a, err := o.own(r, caller)
			if err == nil && check != nil {
				err = check(r, caller, a)
			}
			if err != nil {
				return err
			}
			return pass(w, r, caller)
		})
	}
	handle("POST /agents", o.create)
	handle("GET /agents", pass)
	owned("GET /agents/{id}", nil)
	owned("GET /agents/{id}/records", func(r *http.Request, _, a harness.Agent) error {
		sid := cmp.Or(r.URL.Query().Get("session"), a.Sessions[len(a.Sessions)-1])
		if !slices.Contains(a.Sessions, sid) {
			return fmt.Errorf("session %s is not agent %s's", sid, a.ID)
		}
		r.URL.Path = "/sessions/" + sid + "/records" // where the harness serves a session's records
		return nil
	})
	owned("PUT /agents/{id}/name", nil)
	owned("PUT /agents/{id}/config", func(r *http.Request, caller, a harness.Agent) error {
		var c harness.AgentConfig
		if err := peek(r, &c); err != nil {
			return err
		}
		return within(caller, &c, a.Apps)
	})
	owned("PUT /agents/{id}/apps", func(r *http.Request, caller, _ harness.Agent) error {
		var apps []string
		if err := peek(r, &apps); err != nil {
			return err
		}
		return within(caller, nil, apps)
	})
	handle("PUT /agents/{id}/notify_on_stop", o.notify)
	owned("POST /agents/{id}/stop", nil)
	handle("DELETE /agents/{id}", o.remove)
	o.Handler = mux
	return o, nil
}

// own is the agent the path names, if the caller created it.
func (o *App) own(r *http.Request, caller harness.Agent) (harness.Agent, error) {
	a, err := o.h.Agent(r.PathValue("id"))
	if err == nil && a.Via != harness.AgentVia+caller.ID {
		err = fmt.Errorf("%w: %s", harness.ErrNoAgent, a.ID)
	}
	return a, err
}

// notify sets whether the caller is told when the agent next stops.
func (o *App) notify(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
	a, err := o.own(r, caller)
	if err != nil {
		return err
	}
	var on bool
	if err := json.NewDecoder(r.Body).Decode(&on); err != nil {
		return err
	}
	parent := ""
	if on {
		parent = caller.ID
	}
	if err := o.watch(a.ID, parent); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// remove deletes the agent as the harness's route does, then its watch.
func (o *App) remove(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
	a, err := o.own(r, caller)
	if err == nil {
		err = o.h.DeleteAgent(o.ctx, a.ID, harness.AgentVia+caller.ID)
	}
	if err == nil {
		err = o.watch(a.ID, "")
	}
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	}
	return err
}

// peek reads a request's JSON body into v and leaves the body to be read
// again, by the route the request is passed to.
func peek(r *http.Request, v any) error {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return json.Unmarshal(b, v)
}

// create makes an agent of the caller's: with no config it gets the
// caller's. It adopts no session: the caller's key reaches none.
func (o *App) create(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
	var c create
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		return err
	}
	if c.Session != "" {
		return errors.New("session: an agent you create starts on a session of its own")
	}
	if c.Config == nil {
		c.Config = &caller.Config
	}
	if err := within(caller, c.Config, c.Apps); err != nil {
		return err
	}
	via := harness.AgentVia + caller.ID
	a, err := o.h.CreateAgent(o.ctx, c.AgentCreate, via)
	if err != nil {
		return err
	}
	if c.NotifyOnStop {
		err = o.watch(a.ID, caller.ID)
	}
	if err == nil && c.Message != "" {
		_, _, err = o.h.PostAgent(o.ctx, a.ID, via, "", []harness.Part{{Type: "text/plain", Body: []byte(c.Message)}})
	}
	if err != nil {
		// The caller is told the create failed and may try again: nothing of
		// this try stays, or each try would leave an agent nobody knows of.
		undo := o.h.DeleteAgent(o.ctx, a.ID, via)
		if undo == nil {
			undo = o.h.Delete(o.ctx, a.Sessions[0], via)
		}
		if undo == nil {
			undo = o.watch(a.ID, "")
		}
		if undo != nil {
			log.Printf("orchestrate: undoing the create of agent %s: %v", a.ID, undo)
		}
		return err
	}
	reply(w, map[string]string{"id": a.ID, "session": a.Sessions[0]})
	return nil
}

// within is errEscalate unless a config's workers and a list of apps are all
// among the caller's own. A nil config asks for no worker.
func within(caller harness.Agent, c *harness.AgentConfig, apps []string) error {
	if c != nil && slices.ContainsFunc(c.Workers, func(w statefile.Worker) bool { return !slices.Contains(caller.Config.Workers, w) }) ||
		slices.ContainsFunc(apps, func(a string) bool { return !slices.Contains(caller.Apps, a) }) {
		return errEscalate
	}
	return nil
}

// watch records that parent is told when agent next stops, or with no
// parent that nobody is.
func (o *App) watch(agent, parent string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.watches[agent]; !ok && parent == "" {
		return nil // nobody was to be told
	}
	next := maps.Clone(o.watches) // memory takes the change once the file has it
	if delete(next, agent); parent != "" {
		next[agent] = parent
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.path+".tmp", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(o.path+".tmp", o.path); err != nil {
		return err
	}
	o.watches = next
	return nil
}

// Stopped is the harness's word that an agent's live session ended a run
// closed or failed. The agent that asked is told once; a rollover never
// gets here.
func (o *App) Stopped(a harness.Agent, st statefile.Status) {
	o.mu.Lock()
	parent, ok := o.watches[a.ID]
	o.mu.Unlock()
	if !ok {
		return
	}
	if err := o.watch(a.ID, ""); err != nil {
		log.Printf("orchestrate: %v", err)
	}
	name := ""
	if a.Name != "" {
		name = " (" + a.Name + ")"
	}
	text := fmt.Sprintf(stopped, a.ID, name, a.Sessions[len(a.Sessions)-1], st)
	if _, _, err := o.h.PostAgent(o.ctx, parent, "app:orchestrate", "", []harness.Part{{Type: "text/plain", Body: []byte(text)}}); err != nil {
		log.Printf("orchestrate: telling agent %s that %s stopped: %v", parent, a.ID, err)
	}
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
