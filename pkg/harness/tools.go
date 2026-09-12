package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const defaultSystem = "You are mainplane, an agent"

// systems holds per-model system prompts. Empty until tuning starts.
var systems = map[string]string{}

// System is the system prompt for a model.
func System(model string) string {
	if s, ok := systems[model]; ok {
		return s
	}
	return defaultSystem
}

// Result limits are opencode's: what current models handle in one result.
const (
	maxBytes = 50 << 10
	maxLines = 2000
)

// Worker is the three primitives every worker has, plus where its scratch
// directory is. Tools are built on them here. Write creates missing parents.
type Worker interface {
	Run(ctx context.Context, interpreter, code string) (out []byte, exit int, err error)
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	Scratch() string
}

// ToolSet picks the set a model family was trained on. GPT-5 and GPT-6
// families edit with patches; everyone else with read, write, edit.
func ToolSet(model string) string {
	if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") {
		return "patch"
	}
	return "default"
}

// Tools is the tool set a config record names. Names are checked on Start
// and Configure.
func Tools(name string) []provider.Tool { return toolSets[name] }

func schema(s string) json.RawMessage { return json.RawMessage(s) }

var (
	run   = provider.Tool{Name: "run", Description: "Run code on a worker", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"code":{"type":"string"},"interpreter":{"type":"string"}},"required":["worker","code"]}`)}
	read  = provider.Tool{Name: "read", Description: "Read a file", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"}},"required":["worker","path"]}`)}
	write = provider.Tool{Name: "write", Description: "Write a file", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"},"content":{"type":"string"}},"required":["worker","path","content"]}`)}
	edit  = provider.Tool{Name: "edit", Description: "Edit a file with replacement", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}},"required":["worker","path","old","new"]}`)}
	patch = provider.Tool{Name: "patch", Description: "Apply a patch in the apply_patch format", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"input":{"type":"string"}},"required":["worker","input"]}`)}
)

// toolSets are the named tool lists a config record can name. Every tool takes
// a worker. The set is fixed for the session's life.
var toolSets = map[string][]provider.Tool{
	"default": {run, read, write, edit},
	"patch":   {run, patch},
}

type args struct {
	Worker, Code, Interpreter, Path, Content, Old, New, Input string
}

// execute runs one tool on a worker. A failure is a result the model reads.
func execute(ctx context.Context, w Worker, tool string, a args) statefile.Record {
	r := statefile.Record{Header: statefile.Header{Type: "text/plain"}}
	ok := func(body string) statefile.Record { r.Body = []byte(body); return r }
	fail := func(err error) statefile.Record { return ok("error: " + err.Error()) }
	switch tool {
	case "run":
		out, exit, err := w.Run(ctx, a.Interpreter, a.Code)
		if err != nil {
			return fail(err)
		}
		r.Exit = &exit
		return ok(string(out))
	case "read":
		b, err := w.Read(ctx, a.Path)
		if err != nil {
			return fail(err)
		}
		return ok(string(b))
	case "write":
		if err := w.Write(ctx, a.Path, []byte(a.Content)); err != nil {
			return fail(err)
		}
		return ok("ok")
	case "edit":
		b, err := w.Read(ctx, a.Path)
		if err != nil {
			return fail(err)
		}
		if n := bytes.Count(b, []byte(a.Old)); n != 1 {
			return fail(fmt.Errorf("old matches %d times, need exactly 1", n))
		}
		if err := w.Write(ctx, a.Path, bytes.Replace(b, []byte(a.Old), []byte(a.New), 1)); err != nil {
			return fail(err)
		}
		return ok("ok")
	case "patch":
		if err := applyPatch(ctx, w, a.Input); err != nil {
			return fail(err)
		}
		return ok("ok")
	}
	return fail(fmt.Errorf("unknown tool %s", tool))
}

// clip keeps the head, or the tail, of b within the limits and reports the
// lines cut. A single line over the byte limit is cut mid-line.
func clip(b []byte, tail bool) ([]byte, int) {
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	if len(lines) <= maxLines && len(b) <= maxBytes {
		return b, 0
	}
	if tail {
		slices.Reverse(lines)
	}
	n, size := 0, 0
	for n < len(lines) && n < maxLines && size+len(lines[n])+1 <= maxBytes {
		size += len(lines[n]) + 1
		n++
	}
	if n == 0 {
		if tail {
			return b[len(b)-maxBytes:], len(lines)
		}
		return b[:maxBytes], len(lines)
	}
	kept := lines[:n]
	if tail {
		slices.Reverse(kept)
	}
	return bytes.Join(kept, []byte("\n")), len(lines) - n
}

// finish cuts a result over the limits and keeps the whole where the model can
// reach it: a read's file is already on the worker; anything else is written
// to the worker's scratch under the call id. Run keeps its tail, where the
// error is; the rest keep their head. A non-zero exit is prefixed last, so
// the cut cannot take it.
func finish(ctx context.Context, w Worker, id, tool string, a args, r statefile.Record) statefile.Record {
	kept, cut := clip(r.Body, tool == "run")
	if cut > 0 {
		full := a.Path
		if tool != "read" {
			full = w.Scratch() + "/output/" + id
			if err := w.Write(ctx, full, r.Body); err != nil {
				return statefile.Record{Header: r.Header, Body: []byte("error: saving full output: " + err.Error())}
			}
		}
		r.Truncated, r.FullOutput = true, full
		note := fmt.Sprintf("[%d lines cut, whole output at %s]", cut, full)
		if tool == "run" {
			r.Body = append([]byte(note+"\n"), kept...)
		} else {
			r.Body = append(kept, []byte("\n"+note)...)
		}
	}
	if r.Exit != nil && *r.Exit != 0 {
		r.Body = append(fmt.Appendf(nil, "exit %d\n", *r.Exit), r.Body...)
	}
	return r
}
