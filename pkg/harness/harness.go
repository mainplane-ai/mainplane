// Package harness steps a session: load the chain, build the context, call the
// provider, append what comes back, execute the calls, repeat until the model
// stops calling tools.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const version = "dev"

type Harness struct {
	Sessions  statefile.Sessions
	Providers map[string]provider.Provider
	Workers   map[string]Worker
}

// check rejects a config that names a provider, tool set, or worker this
// harness lacks. Validated on write, trusted on read.
func (h Harness) check(conf statefile.Conf) error {
	if _, ok := h.Providers[conf.Provider]; !ok {
		return fmt.Errorf("unknown provider %q", conf.Provider)
	}
	if _, ok := toolSets[conf.Tools]; !ok {
		return fmt.Errorf("unknown tool set %q", conf.Tools)
	}
	for _, w := range conf.Workers {
		if _, ok := h.Workers[w]; !ok {
			return fmt.Errorf("unknown worker %q", w)
		}
	}
	return nil
}

// Start creates a session: start, config, then the system prompt for the model.
func (h Harness) Start(id string, conf statefile.Conf) error {
	if err := h.check(conf); err != nil {
		return err
	}
	body, err := json.Marshal(conf)
	if err != nil {
		return err
	}
	f, err := h.Sessions.Start(id, body)
	if err != nil {
		return err
	}
	if err := f.Append(statefile.Record{Header: statefile.Header{Kind: statefile.System, Type: "text/plain", Source: "harness"}, Body: []byte(System(conf.Model))}); err != nil {
		return err
	}
	return f.Close()
}

// Configure links a new file with a new config onto a closed session. A
// session with a call outstanding cannot change model or provider: the next
// step would replay foreign thinking, which Anthropic rejects.
func (h Harness) Configure(id string, conf statefile.Conf) error {
	if err := h.check(conf); err != nil {
		return err
	}
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return err
	}
	if st := statefile.Derive(chain); st != statefile.StatusClosed {
		return fmt.Errorf("session %s is %s, config changes need closed", id, st)
	}
	body, err := json.Marshal(conf)
	if err != nil {
		return err
	}
	live, err := h.Sessions.Live(id)
	if err != nil {
		return err
	}
	tip := statefile.Position{Session: id, File: live, N: chain[len(chain)-1].N + 1}
	f, err := h.Sessions.Link(id, tip, statefile.Continue, body)
	if err != nil {
		return err
	}
	return f.Close()
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

// Step runs one cycle and returns the status after it. Interrupted sessions
// get a result for every orphan call and become open; the next cycle steps.
func (h Harness) Step(ctx context.Context, id string) (status statefile.Status, err error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return "", err
	}
	status = statefile.Derive(chain)
	if status == statefile.StatusClosed || status == statefile.StatusFailed {
		return status, nil
	}
	f, err := h.Sessions.Open(id)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if status == statefile.StatusInterrupted {
		for _, c := range statefile.Waiting(chain) {
			if err := f.Append(result(c.ID, "interrupted, effect unknown")); err != nil {
				return "", err
			}
		}
		return statefile.StatusOpen, nil
	}
	conf, err := config(chain)
	if err != nil {
		return "", err
	}
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
			appendErr = f.Append(r)
		}
	})
	if appendErr != nil {
		return "", appendErr
	}
	if err != nil {
		if err := f.Append(statefile.Record{Header: statefile.Header{Kind: statefile.Error, Type: "text/plain"}, Body: []byte(err.Error())}); err != nil {
			return "", err
		}
		return statefile.StatusFailed, nil
	}
	hdr.Kind, hdr.ID, hdr.Upto, hdr.Harness = statefile.Step, stepID, upto, version
	if err := f.Append(statefile.Record{Header: hdr}); err != nil {
		return "", err
	}
	for _, c := range calls {
		if err := f.Append(h.execute(ctx, conf, c)); err != nil {
			return "", err
		}
	}
	if len(calls) == 0 {
		return statefile.StatusClosed, nil
	}
	return statefile.StatusOpen, nil
}

// Run steps until the session is not open.
func (h Harness) Run(ctx context.Context, id string) (statefile.Status, error) {
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
func (h Harness) execute(ctx context.Context, conf statefile.Conf, call statefile.Record) statefile.Record {
	var c provider.Call
	if err := json.Unmarshal(call.Body, &c); err != nil {
		return result(call.ID, "error: "+err.Error())
	}
	var a args
	if err := json.Unmarshal(c.Arguments, &a); err != nil {
		return result(call.ID, "error: "+err.Error())
	}
	w, ok := h.Workers[a.Worker]
	if !ok || !slices.Contains(conf.Workers, a.Worker) {
		return result(call.ID, "error: unknown worker "+a.Worker)
	}
	r := execute(ctx, w, c.Name, a)
	r.Kind, r.For = statefile.Result, call.ID
	return r
}
