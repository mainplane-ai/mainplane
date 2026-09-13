// Package harness steps a session: load the chain, build the context, call the
// provider, append what comes back, execute the calls, repeat until the model
// stops calling tools.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const version = "dev"

type Harness struct {
	Sessions  statefile.Sessions
	Providers map[string]provider.Provider
	Workers   *Pool

	mu sync.Mutex
	s  map[string]*session
}

// check rejects a config that names a provider or tool set this harness lacks.
// Workers are not checked: the config is the whitelist, and a worker named
// before it dials in is a worker the session waits for.
func (h *Harness) check(conf statefile.Conf) error {
	if _, ok := h.Providers[conf.Provider]; !ok {
		return fmt.Errorf("unknown provider %q", conf.Provider)
	}
	if _, ok := toolSets[conf.Tools]; !ok {
		return fmt.Errorf("unknown tool set %q", conf.Tools)
	}
	return nil
}

// Link is one link operation: a new file whose first record points at From.
// Fork, revert, compaction, and config change are all this shape. From
// defaults to the tip of the session linked onto, Mode to continue, Config to
// the config in force at From.
type Link struct {
	From   *statefile.Position `json:"from,omitempty"`
	Mode   statefile.Mode      `json:"mode,omitempty"`
	Config *statefile.Conf     `json:"config,omitempty"`
}

// Create makes a session and returns its id. With From it is a fork; without,
// a fresh session that needs a config, and starts with the system prompt.
func (h *Harness) Create(l Link) (string, error) {
	id := statefile.NewID()
	if l.From != nil {
		_, err := h.Link(id, l)
		return id, err
	}
	if l.Config == nil {
		return "", fmt.Errorf("a fresh session needs a config")
	}
	if err := h.check(*l.Config); err != nil {
		return "", err
	}
	body, err := json.Marshal(l.Config)
	if err != nil {
		return "", err
	}
	f, err := h.Sessions.Start(id, body)
	if err != nil {
		return "", err
	}
	if err := f.Append(statefile.Record{Header: statefile.Header{Kind: statefile.System, Type: "text/plain", Source: "harness"}, Body: []byte(System(l.Config.Model))}); err != nil {
		return "", err
	}
	return id, f.Close()
}

// Link adds a file to session id and returns its number. A session that is
// stepping or has a call outstanding cannot be linked onto: the next step
// would replay what the model never saw.
func (h *Harness) Link(id string, l Link) (int, error) {
	s := h.session(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		return 0, fmt.Errorf("session %s is stepping", id)
	}
	live, err := h.Sessions.Live(id)
	if err != nil {
		return 0, err
	}
	if live > 0 {
		chain, err := h.Sessions.Load(id)
		if err != nil {
			return 0, err
		}
		if st := statefile.Derive(chain); st != statefile.StatusClosed {
			return 0, fmt.Errorf("session %s is %s, links need closed", id, st)
		}
		if l.From == nil {
			l.From = &statefile.Position{Session: id, File: live, N: chain[len(chain)-1].N + 1}
		}
	}
	if l.From == nil {
		return 0, fmt.Errorf("session %s does not exist, a link needs from", id)
	}
	if !statefile.ValidID(l.From.Session) {
		return 0, fmt.Errorf("bad session id %q", l.From.Session)
	}
	if l.Mode == "" {
		l.Mode = statefile.Continue
	}
	if l.Mode != statefile.Continue && l.Mode != statefile.Restart {
		return 0, fmt.Errorf("unknown mode %q", l.Mode)
	}
	if l.Config == nil {
		src, err := h.Sessions.LoadAt(*l.From)
		if err != nil {
			return 0, err
		}
		conf, err := config(src)
		if err != nil {
			return 0, err
		}
		l.Config = &conf
	}
	if err := h.check(*l.Config); err != nil {
		return 0, err
	}
	body, err := json.Marshal(l.Config)
	if err != nil {
		return 0, err
	}
	f, err := h.Sessions.Link(id, *l.From, l.Mode, body)
	if err != nil {
		return 0, err
	}
	if l.Mode == statefile.Restart {
		if err := f.Append(statefile.Record{Header: statefile.Header{Kind: statefile.System, Type: "text/plain", Source: "harness"}, Body: []byte(System(l.Config.Model))}); err != nil {
			return 0, err
		}
	}
	return f.Num, f.Close()
}

func config(chain []statefile.Record) (statefile.Conf, error) {
	var conf statefile.Conf
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Kind == statefile.Config {
			return conf, json.Unmarshal(chain[i].Body, &conf)
		}
	}
	return conf, fmt.Errorf("no config record")
}

// workers is the session's worker list as the model reads it: what each named
// worker is right now, or that it is not connected.
func (h *Harness) workers(names []string) string {
	var b strings.Builder
	for _, name := range names {
		if r, ok := h.Workers.Get(name); ok {
			fmt.Fprintf(&b, "worker %s: %s, %s, scratch %s\n", name, r.OS, strings.Join(r.Interps, " "), r.Scratch)
		} else {
			fmt.Fprintf(&b, "worker %s: not connected\n", name)
		}
	}
	return b.String()
}

// Step runs one cycle and returns the status after it. Interrupted sessions
// get a result for every orphan call and become open; the next cycle steps.
// The worker list enters as a system record whenever it differs from the last
// one the model saw.
func (h *Harness) Step(ctx context.Context, id string) (status statefile.Status, err error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return "", err
	}
	status = statefile.Derive(chain)
	if status == statefile.StatusClosed || status == statefile.StatusFailed {
		return status, nil
	}
	s := h.session(id)
	if err := s.hold(h.Sessions, id); err != nil {
		return "", err
	}
	defer func() {
		if cerr := s.release(); err == nil {
			err = cerr
		}
	}()
	if status == statefile.StatusInterrupted {
		for _, c := range statefile.Waiting(chain) {
			if _, err := s.append(result(c.ID, "interrupted, effect unknown")); err != nil {
				return "", err
			}
		}
		return statefile.StatusOpen, nil
	}
	conf, err := config(chain)
	if err != nil {
		return "", err
	}
	if list := h.workers(conf.Workers); list != last(chain, "workers") {
		if _, err := s.append(statefile.Record{Header: statefile.Header{Kind: statefile.System, Type: "text/plain", Source: "workers"}, Body: []byte(list)}); err != nil {
			return "", err
		}
		if chain, err = h.Sessions.Load(id); err != nil {
			return "", err
		}
	}
	return h.step(ctx, s, id, conf, chain)
}

// step is one provider call: blocks as they stream, the step record, then a
// result for every call.
func (h *Harness) step(ctx context.Context, s *session, id string, conf statefile.Conf, chain []statefile.Record) (statefile.Status, error) {
	records, upto := statefile.Build(chain)
	stepID := statefile.NewID()
	var calls []statefile.Record
	var appendErr error
	req := provider.Request{Model: conf.Model, Key: id, Tools: Tools(conf.Tools), Context: records}
	hdr, err := h.Providers[conf.Provider].Step(ctx, req, func(r statefile.Record) {
		r.Step = stepID
		if r.ID == "" {
			r.ID = statefile.NewID()
		}
		if r.Kind == statefile.Call {
			calls = append(calls, r)
		}
		if appendErr == nil {
			_, appendErr = s.append(r)
		}
	})
	if appendErr != nil {
		return "", appendErr
	}
	if err != nil {
		if _, err := s.append(statefile.Record{Header: statefile.Header{Kind: statefile.Error, Type: "text/plain"}, Body: []byte(err.Error())}); err != nil {
			return "", err
		}
		return statefile.StatusFailed, nil
	}
	hdr.Kind, hdr.ID, hdr.Upto, hdr.Harness = statefile.Step, stepID, upto, version
	if _, err := s.append(statefile.Record{Header: hdr}); err != nil {
		return "", err
	}
	for _, c := range calls {
		if _, err := s.append(h.execute(ctx, id, conf, c)); err != nil {
			return "", err
		}
	}
	if len(calls) == 0 {
		return statefile.StatusClosed, nil
	}
	return statefile.StatusOpen, nil
}

// last is the body of the newest system record from source, or empty.
func last(chain []statefile.Record, source string) string {
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Kind == statefile.System && chain[i].Source == source {
			return string(chain[i].Body)
		}
	}
	return ""
}

// Run steps until the session is not open.
func (h *Harness) Run(ctx context.Context, id string) (statefile.Status, error) {
	for {
		st, err := h.Step(ctx, id)
		if err != nil || st != statefile.StatusOpen {
			return st, err
		}
	}
}

func result(call, text string) statefile.Record {
	return statefile.Record{Header: statefile.Header{Kind: statefile.Result, For: call, Type: "text/plain"}, Body: []byte(text)}
}

// execute routes a call to the worker its arguments name. The session reaches
// only the workers its config lists.
func (h *Harness) execute(ctx context.Context, id string, conf statefile.Conf, call statefile.Record) statefile.Record {
	var c provider.Call
	if err := json.Unmarshal(call.Body, &c); err != nil {
		return result(call.ID, "error: "+err.Error())
	}
	var a args
	if err := json.Unmarshal(c.Arguments, &a); err != nil {
		return result(call.ID, "error: "+err.Error())
	}
	if !slices.Contains(conf.Workers, a.Worker) {
		return result(call.ID, "error: unknown worker "+a.Worker)
	}
	w, ok := h.Workers.Get(a.Worker)
	if !ok {
		return result(call.ID, "error: worker "+a.Worker+" is not connected")
	}
	r, dropped := execute(ctx, w, id, c.Name, a)
	r = finish(c.Name, a, r, dropped)
	r.Kind, r.For = statefile.Result, call.ID
	return r
}
