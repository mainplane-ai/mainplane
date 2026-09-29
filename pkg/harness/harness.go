// Package harness steps a session: load the file, build the context, call the
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
	"github.com/mainplane-ai/mainplane/pkg/version"
)

type Harness struct {
	Sessions  statefile.Sessions
	Providers map[string]provider.Provider
	Workers   *Pool
	Remove    func(name string) error // the coordinator's: the worker leaves the mesh for good

	mu sync.Mutex
	s  map[string]*session

	imu   sync.Mutex
	index map[string]Info
	dirty map[string]bool
}

// provider splits a config's provider/model string and finds the provider.
// The model part is what the provider is asked for; an openrouter-style id
// with its own slash survives, because only the first is cut.
func (h *Harness) provider(model string) (provider.Provider, string, error) {
	name, m, ok := strings.Cut(model, "/")
	p, known := h.Providers[name]
	if !ok || !known || m == "" {
		return provider.Provider{}, "", fmt.Errorf("model %q is not provider/model with a known provider", model)
	}
	return p, m, nil
}

// Create is the body of POST /sessions. From set is a copy of records 1
// through N-1 of that session; otherwise a fresh session from Model, Context,
// and Workers. Workers are not checked: the config is the whitelist, and a
// worker named before it dials in is a worker the session waits for.
type Create struct {
	From    string             `json:"from,omitempty"`
	N       int                `json:"n,omitempty"`
	Model   string             `json:"model,omitempty"`
	Context int                `json:"context,omitempty"`
	Workers []statefile.Worker `json:"workers,omitempty"`
	Input   []string           `json:"input,omitempty"`
}

func system(source, body string) statefile.Record {
	return statefile.Record{Header: statefile.Header{Kind: statefile.System, Type: "text/plain", Source: source}, Body: []byte(body)}
}

// Create makes a session and returns its id. A fresh one starts with the
// system prompt and the AGENTS.md scan. A copy is byte for byte and gets
// nothing appended: it already holds both.
func (h *Harness) Create(ctx context.Context, c Create) (string, error) {
	id := statefile.NewID()
	if c.From != "" {
		if c.Model != "" || c.Context != 0 || c.Workers != nil || c.Input != nil {
			return "", fmt.Errorf("a copy takes no config")
		}
		if err := h.cut(c.From, c.N); err != nil {
			return "", err
		}
		if err := h.Sessions.Copy(c.From, id, c.N); err != nil {
			return "", err
		}
		h.touch(id)
		return id, nil
	}
	p, model, err := h.provider(c.Model)
	if err != nil {
		return "", err
	}
	if c.Context <= 0 {
		return "", fmt.Errorf("context limit must be set")
	}
	for _, t := range c.Input {
		if !p.Envelope.Accepts(t) {
			return "", fmt.Errorf("%s does not take %s as input", p.Envelope.Name(), t)
		}
	}
	conf := statefile.Conf{Model: c.Model, Tools: ToolSet(model), Workers: c.Workers, Context: c.Context, Input: c.Input}
	if conf.Workers == nil {
		conf.Workers = []statefile.Worker{}
	}
	body, err := json.Marshal(conf)
	if err != nil {
		return "", err
	}
	f, err := h.Sessions.Start(id, body)
	if err != nil {
		return "", err
	}
	if err := f.Append(system("harness", System(model))); err != nil {
		return "", err
	}
	if err := f.Append(system("agents", h.agents(ctx, id, conf))); err != nil {
		return "", err
	}
	h.touch(id)
	return id, f.Close()
}

// cut refuses a copy a provider would reject: the cut must fall before a
// message record or at the tip of a closed session, so no call is left
// without its result and no step is split.
func (h *Harness) cut(from string, n int) error {
	if !statefile.ValidID(from) {
		return fmt.Errorf("bad session id %q", from)
	}
	chain, err := h.Sessions.Load(from)
	if err != nil {
		return err
	}
	if n <= 2 {
		return fmt.Errorf("n must keep start and config")
	}
	if n <= len(chain) {
		if chain[n-1].Kind != statefile.Message {
			return fmt.Errorf("record %d is a %s, a cut must fall before a message or at a closed tip", n, chain[n-1].Kind)
		}
		if len(statefile.Waiting(chain[:n-1])) > 0 {
			return fmt.Errorf("record %d was posted mid-step, a cut there leaves a call without its result", n)
		}
		return nil
	}
	if n != len(chain)+1 {
		return fmt.Errorf("session %s has %d records", from, len(chain))
	}
	if st := h.status(from, chain); st != statefile.StatusClosed {
		return fmt.Errorf("session %s is %s, a cut at the tip needs closed", from, st)
	}
	return nil
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
// worker is right now and the drives it has, or that it is not connected.
func (h *Harness) workers(workers []statefile.Worker) string {
	var b strings.Builder
	for _, w := range workers {
		if r, ok := h.Workers.Get(w.Name); ok {
			fmt.Fprintf(&b, "worker %s: %s, %s, scratch %s, drives %q\n", w.Name, r.OS, strings.Join(r.Interps, " "), r.Scratch, w.Drives)
		} else {
			fmt.Fprintf(&b, "worker %s: not connected\n", w.Name)
		}
	}
	return b.String()
}

// Step runs one cycle and returns the status after it. A failed session is
// stepped only when a run asked for it; every step spends the ask, so a run
// on a closed session does not carry over to a later failure. Interrupted sessions get a result for
// every orphan call and become open; the next cycle steps. Past the context
// limit nothing is called: the error says so and the session is failed. The
// worker list enters as a system record whenever it differs from the last one
// the model saw.
func (h *Harness) Step(ctx context.Context, id string) (status statefile.Status, err error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return "", err
	}
	s := h.session(id)
	status = statefile.Derive(chain)
	if retry := s.takeRetry(); status == statefile.StatusClosed || status == statefile.StatusFailed && !retry {
		return status, nil
	}
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
			if _, err := h.append(s, result(c.ID, "interrupted, effect unknown")); err != nil {
				return "", err
			}
		}
		return statefile.StatusOpen, nil
	}
	conf, err := config(chain)
	if err != nil {
		return "", err
	}
	if p := statefile.Prompt(chain); p > conf.Context {
		_, err := h.append(s, errorRecord("", fmt.Sprintf("context %d exceeds limit %d", p, conf.Context)))
		return statefile.StatusFailed, err
	}
	if list := h.workers(conf.Workers); list != last(chain, "workers") {
		if _, err := h.append(s, system("workers", list)); err != nil {
			return "", err
		}
		if chain, err = h.Sessions.Load(id); err != nil {
			return "", err
		}
	}
	return h.step(ctx, s, id, conf, chain)
}

func errorRecord(via, text string) statefile.Record {
	return statefile.Record{Header: statefile.Header{Kind: statefile.Error, Type: "text/plain", Via: via}, Body: []byte(text)}
}

// step is one provider call: blocks as they stream, the step record, then a
// result for every call. A stop cancels ctx: the stream ends without a step
// record, every call not yet answered gets a stopped result, and the last
// record is an error naming who stopped it.
func (h *Harness) step(ctx context.Context, s *session, id string, conf statefile.Conf, chain []statefile.Record) (statefile.Status, error) {
	p, model, err := h.provider(conf.Model)
	if err != nil {
		return "", err
	}
	records, upto := statefile.Build(chain)
	stepID := statefile.NewID()
	var calls []statefile.Record
	var appendErr error
	req := provider.Request{Model: model, Key: id, Tools: Tools(conf.Tools, conf.Input), Context: records}
	hdr, err := p.Step(ctx, req, func(r statefile.Record) {
		r.Step = stepID
		if r.ID == "" {
			r.ID = statefile.NewID()
		}
		if r.Kind == statefile.Call {
			calls = append(calls, r)
		}
		if appendErr == nil {
			_, appendErr = h.append(s, r)
		}
	})
	if appendErr != nil {
		return "", appendErr
	}
	if err != nil {
		if via := s.stoppedBy(); via != "" {
			_, err := h.append(s, errorRecord(via, "stopped via "+via))
			return statefile.StatusFailed, err
		}
		_, err := h.append(s, errorRecord("", err.Error()))
		return statefile.StatusFailed, err
	}
	hdr.Kind, hdr.ID, hdr.Upto, hdr.Harness = statefile.Step, stepID, upto, version.V
	if _, err := h.append(s, statefile.Record{Header: hdr}); err != nil {
		return "", err
	}
	for _, c := range calls {
		var r statefile.Record
		if ctx.Err() == nil {
			r = h.execute(ctx, id, conf, c)
		}
		if ctx.Err() != nil {
			r = result(c.ID, "stopped, effect unknown")
		}
		if _, err := h.append(s, r); err != nil {
			return "", err
		}
	}
	if via := s.stoppedBy(); via != "" {
		_, err := h.append(s, errorRecord(via, "stopped via "+via))
		return statefile.StatusFailed, err
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
	if !slices.ContainsFunc(conf.Workers, func(w statefile.Worker) bool { return w.Name == a.Worker }) {
		return result(call.ID, "error: unknown worker "+a.Worker)
	}
	w, ok := h.Workers.Get(a.Worker)
	if !ok {
		return result(call.ID, "error: worker "+a.Worker+" is not connected")
	}
	r, dropped := execute(ctx, w, id, c.Name, a, conf.Input)
	r = finish(c.Name, a, r, dropped)
	r.Kind, r.For = statefile.Result, call.ID
	return r
}
