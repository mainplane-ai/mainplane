// Package harness steps a session: load the file, build the context, call the
// provider, append what comes back, execute the calls, repeat until the model
// stops calling tools.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// halted is the body of a stop's error record, with who stopped it. The model
// reads it only when a retry or a new message steps the session again; a
// retry adds resume as a message, so the model goes on rather than stopping.
// abnormal is the error after a step with no call that the model did not end
// itself: without it the session would close on a cut-off, refused, or
// filtered reply, or on thinking alone.
const (
	halted   = "The session was stopped during the last step by %s. Running calls were cancelled."
	abnormal = "The last step ended with stop %q (%d output tokens) and no call, so its reply may be cut short, refused, or missing. If the stop is the output token limit, raise the provider's output token limit in params. Retry to continue."
	resume   = "Continue"
)

type Harness struct {
	Sessions  statefile.Sessions
	Providers map[string]provider.Provider // read and replaced under pmu once the harness runs
	Workers   *Pool
	Remove    func(name string) error // the coordinator's: the worker leaves the mesh for good

	pmu sync.RWMutex
	mu  sync.Mutex
	s   map[string]*session

	imu   sync.Mutex
	index map[string]Info
	dirty map[string]bool
}

// provider splits a config's provider/model string and finds the provider.
// The model part is what the provider is asked for; an openrouter-style id
// with its own slash survives, because only the first is cut. The model part
// is not checked: the provider rejects one it does not know at the first step.
func (h *Harness) provider(model string) (provider.Provider, string, error) {
	name, m, ok := strings.Cut(model, "/")
	if !ok || name == "" || m == "" {
		return provider.Provider{}, "", fmt.Errorf("model %q is not provider/model", model)
	}
	if _, ok := provider.Supported[name]; !ok {
		return provider.Provider{}, "", fmt.Errorf("unknown provider %q; supported: %s", name, strings.Join(slices.Sorted(maps.Keys(provider.Supported)), ", "))
	}
	p, ok := h.providers()[name]
	if !ok {
		return provider.Provider{}, "", fmt.Errorf("provider %q has no key: mainplane key %s <key>", name, name)
	}
	return p, m, nil
}

// SetProviders replaces the providers: a session's next step uses the new
// ones, a step running keeps its own.
func (h *Harness) SetProviders(ps map[string]provider.Provider) {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	h.Providers = ps
}

func (h *Harness) providers() map[string]provider.Provider {
	h.pmu.RLock()
	defer h.pmu.RUnlock()
	return h.Providers
}

// Create is the body of POST /sessions. From set is a copy of records 1
// through N-1 of that session; otherwise a fresh session from Model,
// ContextLimit, and Workers. Workers are not checked: the config is the
// whitelist, and a worker named before it dials in is a worker the session
// waits for.
type Create struct {
	From         string                     `json:"from,omitempty"`
	N            int                        `json:"n,omitempty"`
	Model        string                     `json:"model,omitempty"`
	ContextLimit int                        `json:"context_limit,omitempty"`
	Workers      []statefile.Worker         `json:"workers,omitempty"`
	Input        []string                   `json:"input,omitempty"`
	Params       map[string]json.RawMessage `json:"params,omitempty"`
}

func system(source, body string) statefile.Record {
	return statefile.Record{Header: statefile.Header{Kind: statefile.System, Type: "text/plain", Source: source}, Body: []byte(body)}
}

// Create makes a session and returns its id. A fresh one starts with the
// system prompt, the AGENTS.md scan, and the worker list, so all three reach
// the provider as system text ahead of the first message. A copy is byte for byte and gets
// nothing appended: it already holds both.
func (h *Harness) Create(ctx context.Context, c Create) (string, error) {
	id := statefile.NewID()
	if c.From != "" {
		if c.Model != "" || c.ContextLimit != 0 || c.Workers != nil || c.Input != nil || c.Params != nil {
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
	if c.ContextLimit <= 0 {
		return "", fmt.Errorf("context_limit must be set")
	}
	for _, t := range c.Input {
		if !p.Envelope.Accepts(t) {
			return "", fmt.Errorf("%s does not take %s as input", p.Envelope.Name(), t)
		}
	}
	for k := range c.Params {
		if p.Envelope.Owns(k) {
			return "", fmt.Errorf("%s builds %s from the session, a param cannot set it", p.Envelope.Name(), k)
		}
	}
	conf := statefile.Conf{Model: c.Model, Tools: ToolSet(model), Workers: c.Workers, ContextLimit: c.ContextLimit, Input: c.Input, Params: c.Params}
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
	if err := f.Append(system("workers", workersFirst+h.workers(conf.Workers))); err != nil {
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

// The worker list's lead line says whether it is the first or a change.
const (
	workersFirst   = "Workers:\n"
	workersChanged = "Workers (changed):\n"
)

// workers is the session's worker list as the model reads it: what each named
// worker is right now and the drives it mounts, each it also serves marked,
// or that it is not connected.
func (h *Harness) workers(workers []statefile.Worker) string {
	var b strings.Builder
	for _, w := range workers {
		if r, ok := h.Workers.Get(w.Name); ok {
			fmt.Fprintf(&b, "worker %s: %s, %s, scratch %s\n", w.Name, r.OS, strings.Join(r.Interps, " "), r.Scratch)
			ds := r.Drives()
			for _, d := range ds {
				if d.Serve {
					continue
				}
				fmt.Fprintf(&b, "  drive %v", d)
				if slices.ContainsFunc(ds, func(s worker.Drive) bool { return s.Serve && s.State == worker.Serving && s.Name == d.Name }) {
					b.WriteString(", served from this worker")
				}
				b.WriteString("\n")
			}
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
// limit nothing is called: the error says so and the session is failed. A
// retry of a failed session adds resume as a message from retry. The worker
// list enters as a system record whenever it differs from the last one the
// model saw.
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
	if p := statefile.ContextUsed(chain); p > conf.ContextLimit {
		_, err := h.append(s, errorRecord("", fmt.Sprintf("context %d exceeds limit %d", p, conf.ContextLimit)))
		return statefile.StatusFailed, err
	}
	var add []statefile.Record
	if status == statefile.StatusFailed {
		add = append(add, statefile.Record{Header: statefile.Header{Kind: statefile.Message, Type: "text/plain", Via: "retry"}, Body: []byte(resume)})
	}
	if list := h.workers(conf.Workers); list != strings.TrimPrefix(strings.TrimPrefix(last(chain, "workers"), workersFirst), workersChanged) {
		add = append(add, system("workers", workersChanged+list))
	}
	for _, r := range add {
		if _, err := h.append(s, r); err != nil {
			return "", err
		}
	}
	if len(add) > 0 {
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
	records = clock(records, id, chain[0].Time, conf.ContextLimit)
	stepID := statefile.NewID()
	var calls []statefile.Record
	var appendErr error
	req := provider.Request{Model: model, Key: id, Tools: Tools(conf.Tools, conf.Input), Context: records, Params: conf.Params}
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
			_, err := h.append(s, errorRecord(via, fmt.Sprintf(halted, via)))
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
		_, err := h.append(s, errorRecord(via, fmt.Sprintf(halted, via)))
		return statefile.StatusFailed, err
	}
	if len(calls) == 0 && !provider.Normal(hdr) {
		_, err := h.append(s, errorRecord("", fmt.Sprintf(abnormal, hdr.Stop, hdr.Usage.Output)))
		return statefile.StatusFailed, err
	}
	if len(calls) == 0 {
		return statefile.StatusClosed, nil
	}
	return statefile.StatusOpen, nil
}

// clock gives the model time and context from the file alone, never the
// clock, so the same file builds the same bytes and the cached prefix holds.
// The session line follows the system prompt and never changes; the system
// prompt says what the notes mean. Each result ends with its time since start
// and the prompt size of the step that made its call, fixed once the result
// is written.
func clock(ctx []statefile.Record, id string, start time.Time, limit int) []statefile.Record {
	used := 0
	for i, r := range ctx {
		if r.Kind == statefile.Step {
			used = r.Usage.Prompt()
		}
		if r.Kind == statefile.Result {
			ctx[i].Note = fmt.Sprintf("time %s context %d", r.Time.Sub(start).Round(time.Second), used)
		}
	}
	line := fmt.Sprintf("Session %s started %s. Context limit %d", id, start.Format(time.RFC3339), limit)
	return slices.Insert(ctx, 1, system("session", line))
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
	if a.Worker == worker.Harness { // a model may take the mesh's name for the machine
		return result(call.ID, "error: harness is the harness's address on the mesh, not a worker; the worker on the harness's machine is "+worker.Admin)
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
