// Package orchestrate is the built-in app that gives an agent the /agents
// routes for agents of its own: it spawns them, reads them and stops them,
// and none of them may reach a worker or an app that it cannot.
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
	"strconv"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

//go:embed skill.md
var Skill string

// stopped is what a watching agent is told, once.
const stopped = "Agent %s%s, which you spawned, stopped: its session %s is %s."

// App is the handler and the watches: which agent to tell when a spawned
// one stops.
type App struct {
	http.Handler
	ctx  context.Context
	h    *harness.Harness
	path string

	mu      sync.Mutex
	watches map[string]string // spawned agent -> the agent that asked to be told
}

// change is the body of PUT /{id}, and with a message that of POST /spawn.
type change struct {
	Name         *string              `json:"name"`
	Config       *harness.AgentConfig `json:"config"`
	Apps         *[]string            `json:"apps"`
	NotifyOnStop *bool                `json:"notify_on_stop"`
	Message      string               `json:"message"`
}

// errEscalate is a spawn or change that asks for more than the caller has.
var errEscalate = errors.New("an agent you spawn may use only workers and apps you have")

// New loads the watches in dir and is the app.
//
//	POST   /spawn         {name, config, apps, message, notify_on_stop}   -> {"id","session"}
//	GET    /              -> [AgentInfo] of the agents the caller spawned
//	GET    /{id}          -> AgentInfo
//	GET    /{id}/records  ?session= ?after=N                               -> records
//	PUT    /{id}          {name, config, apps, notify_on_stop}             -> 204
//	POST   /{id}/stop     -> 204
//	DELETE /{id}          -> 204
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
	mux := http.NewServeMux()
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
	handle("POST /spawn", o.spawn)
	handle("GET /{$}", func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
		reply(w, slices.DeleteFunc(h.ListAgents(), func(i harness.AgentInfo) bool { return i.Via != "agent:"+caller.ID }))
		return nil
	})
	handle("GET /{id}", func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
		a, err := o.own(r, caller)
		if err != nil {
			return err
		}
		info, err := h.AgentInfo(a.ID)
		if err == nil {
			reply(w, info)
		}
		return err
	})
	handle("GET /{id}/records", o.records)
	handle("PUT /{id}", o.update)
	handle("POST /{id}/stop", func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
		a, err := o.own(r, caller)
		if err == nil {
			err = h.Stop(ctx, a.Sessions[len(a.Sessions)-1], "agent:"+caller.ID)
		}
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
		}
		return err
	})
	handle("DELETE /{id}", func(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
		a, err := o.own(r, caller)
		if err == nil {
			err = h.DeleteAgent(ctx, a.ID, "agent:"+caller.ID)
		}
		if err == nil {
			err = o.watch(a.ID, "")
		}
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
		}
		return err
	})
	o.Handler = mux
	return o, nil
}

// own is the agent the path names, if the caller spawned it.
func (o *App) own(r *http.Request, caller harness.Agent) (harness.Agent, error) {
	a, err := o.h.Agent(r.PathValue("id"))
	if err == nil && a.Via != "agent:"+caller.ID {
		err = fmt.Errorf("%w: %s", harness.ErrNoAgent, a.ID)
	}
	return a, err
}

// spawn makes an agent of the caller's: with no config it gets the caller's.
func (o *App) spawn(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
	var c change
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		return err
	}
	create := harness.AgentCreate{Config: &caller.Config}
	if c.Name != nil {
		create.Name = *c.Name
	}
	if c.Config != nil {
		create.Config = c.Config
	}
	if c.Apps != nil {
		create.Apps = *c.Apps
	}
	if !within(caller, create.Config, create.Apps) {
		return errEscalate
	}
	a, err := o.h.CreateAgent(o.ctx, create, "agent:"+caller.ID)
	if err != nil {
		return err
	}
	if c.NotifyOnStop != nil && *c.NotifyOnStop {
		if err := o.watch(a.ID, caller.ID); err != nil {
			return err
		}
	}
	if c.Message != "" {
		if _, _, err := o.h.PostAgent(o.ctx, a.ID, "agent:"+caller.ID, "", []harness.Part{{Type: "text/plain", Body: []byte(c.Message)}}); err != nil {
			return err
		}
	}
	reply(w, map[string]string{"id": a.ID, "session": a.Sessions[0]})
	return nil
}

// records are a session of a spawned agent, the live one unless one is named.
func (o *App) records(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
	a, err := o.own(r, caller)
	if err != nil {
		return err
	}
	sid := a.Sessions[len(a.Sessions)-1]
	if s := r.URL.Query().Get("session"); s != "" {
		sid = s
	}
	if !slices.Contains(a.Sessions, sid) {
		return fmt.Errorf("session %s is not agent %s's", sid, a.ID)
	}
	chain, err := o.h.Sessions.Load(sid)
	if err != nil {
		return err
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	w.Header().Set("Content-Type", "application/x-mainplane-records")
	for _, rec := range chain[min(max(after, 0), len(chain)):] {
		b, err := statefile.Encode(rec)
		if err != nil {
			return err
		}
		_, _ = w.Write(b)
	}
	return nil
}

// update changes what the body names and leaves the rest.
func (o *App) update(w http.ResponseWriter, r *http.Request, caller harness.Agent) error {
	a, err := o.own(r, caller)
	if err != nil {
		return err
	}
	var c change
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		return err
	}
	apps := a.Apps
	if c.Apps != nil {
		apps = *c.Apps
	}
	if !within(caller, c.Config, apps) {
		return errEscalate
	}
	if c.Name != nil {
		err = o.h.SetAgentName(a.ID, *c.Name)
	}
	if err == nil && c.Config != nil {
		err = o.h.SetAgentConfig(a.ID, "agent:"+caller.ID, *c.Config)
	}
	if err == nil && c.Apps != nil {
		err = o.h.SetAgentApps(a.ID, apps)
	}
	if err == nil && c.NotifyOnStop != nil {
		parent := ""
		if *c.NotifyOnStop {
			parent = caller.ID
		}
		err = o.watch(a.ID, parent)
	}
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	}
	return err
}

// within is whether a config's workers and a list of apps are all among the
// caller's own. A nil config asks for no worker.
func within(caller harness.Agent, c *harness.AgentConfig, apps []string) bool {
	if c != nil {
		for _, w := range c.Workers {
			if !slices.Contains(caller.Config.Workers, w) {
				return false
			}
		}
	}
	return !slices.ContainsFunc(apps, func(a string) bool { return !slices.Contains(caller.Apps, a) })
}

// watch records that parent is told when agent next stops, or with no
// parent that nobody is.
func (o *App) watch(agent, parent string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if delete(o.watches, agent); parent != "" {
		o.watches[agent] = parent
	}
	b, err := json.MarshalIndent(o.watches, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(o.path+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(o.path+".tmp", o.path)
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
