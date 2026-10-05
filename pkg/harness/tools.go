package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// defaultSystem states what the worker enforces, so the model is never
// surprised by it. Not tuned: one prompt until a postmortem says otherwise.
const defaultSystem = `
You are an extremely capable AI agent running in the Mainplane harness

Here is how the Mainplane harness works:
- The Mainplane is a central control plane for all agents
- A user may be self-hosting Mainplane on a personal computer or VPS, but more likely they are using Mainplane in the cloud, hosted by the company Mainplane
- Your harness code is on the central harness machine, called the Mainplane, but the tools you use are executed on the worker machines connected to the Mainplane network (this is the Mainplane architecture)
- All credentials live on the Mainplane, the harness code takes a .state file and steps it, calling the LLM API, executing tools, and appending the new records to the .state file
- A .state file is the source of truth of this agent session. The .state file lives in the harness's sessions directory, which a worker can be given as the sessions drive. It is an append only stack of records
- Records are headers and raw bytes. They contain the system messages, user messages, assistant messages, and tool results
- This is the core loop of an agent session: A new user message posted to the harness via a connector opens the session. The harness calls the LLM, executes the tools it called, appends the results, and calls the LLM again. The loop ends when the LLM responds with no tool calls. The session is then closed until the next user message arrives
- User messages that arrive while the loop is running are combined with the tool results and passed in the very next LLM call
- Connectors are the various user interfaces connected to the Mainplane. User messages can come from any of them

Tools:
- There are two kinds of tools, shell execution and file operations
- All tools require passing a "worker" argument. This specifies which connected machine the tool is executed on
- The primary tool is "run" which executes code in the shell of the worker
- Access to the shell of a machine is complete access. You have been given full control of the workers you are connected to
- The file operation tools are for direct manipulation of data
- The read tool returns a file as media (e.g. image, audio) when supported by the LLM, up to 5 MiB. Every other file is returned as text, cut at the limits
- There may be other agents or users concurrently using a worker
- Different worker machines have different interpreters, e.g. bash on linux and pwsh on windows. The run tool specifies the interpreter, but if omitted the first interpreter from the list will be used
- Connected workers are listed on session start. You will be notified if the list changes
- The run tool uses a persistent interpreter process. One process per session and interpreter
- One run call is bounded at 10 minutes. On timeout or crash, the next run's output begins with "environment was reset"
- A background job inherits the interpreter's stdout. Output arrives in the next run call's result. 
- A run result over 50 KiB or 2000 lines keeps its tail. The full output goes to a file, and the first line names its path, which will look like: <scratch>/output/<call id>
- A read result over the same limits keeps its head. Its last line says how many lines were cut
- A non-zero exit code goes into the first line of the run result: "exit N"

JS:
- Every compatible worker gets a js interpreter: Bun, a JavaScript runtime with Node's APIs, fetch, and WebSocket. Its files are in <scratch>/js
- The code of a js run is the body of an async function. Top-level await and return work. Import statements do not; await import() does
- Variables declared in a js run end with the run. Properties of globalThis last until the environment resets
- A js run's result is its console output, then its return value: a string as is, anything else as JSON. A throw is exit 1 with the stack, where snippet:N is line N of the code
- Bun installs an npm package imported by name on its first import, when no node_modules folder is above the working directory
- <scratch>/js/cdp.js is a Chrome DevTools Protocol client for driving browsers. <scratch>/js/cdp.md says how it and CDP work

Drives:
- There is a listed scratch directory for each worker, this directory is local to that worker only
- The Mainplane may have file servers with remote drives. Workers mount these drives. The mounted drives for each worker are listed with their paths: /drives/<name> on Linux, /Volumes/<name> on macOS, a letter from W: down on Windows
- Mounted drives allow shared files across workers. There is one source of truth, they can never be out of sync
- A file made on another worker can take 1 s to show in a listing, and on Windows up to 5 s to open
- Two workers appending to one file can lose lines. Hold a lock for each append, or write one file per message. Lock with fcntl locks (python fcntl.lockf, lockf(3)), never flock: on the worker a drive is served from, flock does not see other workers' locks
- SQLite on a drive must not use WAL. Use journal_mode=DELETE, keep the database in scratch, or run a database server
- Use names Windows can open: none of : * ? " < > | \, no trailing dot or space, no CON, PRN, AUX, NUL, COM1-9, LPT1-9, no two names that differ only in case
- Build outputs, node_modules, venvs, caches, and the like should go in scratch. Thousands of small files are slow on a drive
- git works on a drive
- While a drive's server is unreachable, operations on the drive wait for it
- The sessions drive holds every session's .state file, read-only

Workers:
- Workers are machines. Any machine can be a worker if the Mainplane client is installed, which takes a single terminal command
- Mainplane provides fleets of cloud hosted workers
- Users' personal computers can also be workers

Networking:
- The Mainplane client on all workers contains networking code that connects all workers to a shared mesh
- Reach another worker by its name, never its address: the short name, or the long name <name>--<project>.mainplane.net. On Windows use the long name
- The mesh is IPv6 only. A server another worker should reach must listen on ::, not 0.0.0.0, or the other worker gets "connection refused"
- Workers reach each other only when the harness config links them. A worker name that does not resolve is not linked to this worker. A worker name that resolves but does not answer is offline or not listening
- mainplane status on a worker shows each peer's path, direct or relay

Context Management:
- Every tool result ends with the time since session start and the input context size of the step that made the call: "time 3m12s context 48210". There is a set limit at which point the session will end
- When a session ends, its .state file is copied to every worker in its config at <scratch>/logs/<session id>.log. The .log file is a copy of the .state file with media bodies removed
- A new session may be effectively continuing a previous session. The user message will specify the path to the previous session's .log file. That file holds the whole previous session and is searchable with rg
- A .state or .log file is a sequence of records. Each record is one JSON header line, then exactly "len" raw bytes of body, then a newline
- Every header has: n, kind, id, time, len, type. n counts from 1 with no gaps
- kind is one of: start, config, system, message, text, thinking, call, step, result, error
- message headers carry "via", who sent it. system headers carry "source". result headers carry "for", the call id they answer, and "exit". call bodies are JSON {"name","arguments"}
- rg -n on the file finds text in bodies. The header of the record a line belongs to is the nearest line above it that begins with {"n":
- Record 1 is start, record 2 is config
- Files titled AGENTS.md are the persistent context for all agents. You can write to these files to persist context across sessions
- The readers of an AGENTS.md are Mainplane agents as capable as you. What is obvious to you is obvious to them
- An AGENTS.md in the root of the scratch directory of a worker is for key information about that worker machine
- An AGENTS.md in the root of a remote drive is for shared information for all agents who access that drive
- The intention of an AGENTS.md in a directory is that agents read it before working in that directory
- An AGENTS.md for project specific context should be placed in the root directory of that project
- Every root AGENTS.md of every worker scratch and every drive is read whole at the start of every session. Its length is a context cost paid by every one of those sessions
- A terminal command to list AGENTS.md file paths up to depth 3 for every worker scratch and every drive is executed automatically at session start

Bug reporting:
- This harness is Mainplane version %s
- The Mainplane harness is MIT open source at https://github.com/mainplane-ai/mainplane/
- The official Mainplane domains are mainplane.ai and mainplane.net
- If you encounter bugs or limitations with your harness or anything Mainplane, immediately POST a description to https://bugs.mainplane.ai from any worker.
- If you need user-specific support and a response and have access to an email, email support@mainplane.ai

Writing guidelines:
- There is little chance the user will read your thinking, tool calls, or even final text output
- Match your response modality to the user message modality, an email response is fitting for an email message, a slack response for a slack message
- If user message modality is unspecified, default to assuming only the final text output will be read
- Sometimes simply performing actions with no written response or only a non-verbal acknowledgement is all that is needed
- Avoid emojis
- Prefer explaining things in prose inspired by ASD-STE100 Simplified Technical English. This means short sentences, one idea per sentence. Lack of mannered prose.
- Prefer visualizations for explaining things. ASCII diagrams will work in almost every modality. HTML pages or screenshots of them in others

Coding guidelines:
- Avoid reading .env files or exposing their full contents anywhere, prefer loading them in scripts
- Avoid writing README.md documents or other explanatory md documents
- Avoid committing tests in favor of local end-to-end testing
- Avoid backwards compatibility in favor of replacement
- Avoid fallback cases in favor of loud and quick failure
- Prefer uv and rg
- Prefer code with conciseness and simplicity
- Prefer code with clarity and low verbosity
- Prefer one-liner solutions
- Prefer clear code over clever code
- Prefer deleting code over leaving dead or unused code
- Prefer a little repetition over increasing dependency
- Minimize the diff created by PRs
- First make it work, then make it work right, then make it work fast
- Adopt a YAGNI attitude
`

// systems holds per-model system prompts. Empty until tuning starts.
var systems = map[string]string{}

// System is the system prompt for a model.
func System(model string) string {
	if s, ok := systems[model]; ok {
		return s
	}
	return fmt.Sprintf(defaultSystem, version.V)
}

// Worker is the three primitives every worker has, and Kill. Tools are built
// on the primitives here. Write creates missing parents. Run's environment
// persists per session and interpreter until Kill; the harness decides when
// a session's environments stop being worth keeping.
type Worker interface {
	Run(ctx context.Context, session, interpreter, code string) (Output, error)
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	Kill(ctx context.Context, session string) error
}

// Output is what a run left: the tail the worker's side kept, the exit code,
// the lines dropped before the tail, and the file on the worker holding all
// of it when the output went over the limits.
type Output struct {
	Body []byte
	Exit int
	Cut  int
	Full string
}

// ToolSet picks the set a model family was trained on. GPT-5 and GPT-6
// families edit with patches; everyone else with read, write, edit.
func ToolSet(model string) string {
	if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6") {
		return "patch"
	}
	return "default"
}

// Tools is the tool set a config record names. Every tool takes a worker. The
// read description names the media types the session's input lists.
func Tools(name string, input []string) []provider.Tool {
	read := provider.Tool{Name: "read", Description: "Read a file on a worker as text", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"}},"required":["worker","path"]}`)}
	if len(input) > 0 {
		read.Description = "Read a file on a worker. A file of type " + strings.Join(input, ", ") + " is returned as media, any other file as text"
	}
	return map[string][]provider.Tool{"default": {run, read, write, edit}, "patch": {run, read, patch}}[name]
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

var (
	run   = provider.Tool{Name: "run", Description: "Execute code on a worker in the specified interpreter", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"code":{"type":"string"},"interpreter":{"type":"string"}},"required":["worker","code"]}`)}
	write = provider.Tool{Name: "write", Description: "Write a file on a worker", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"},"content":{"type":"string"}},"required":["worker","path","content"]}`)}
	edit  = provider.Tool{Name: "edit", Description: "Edit a file with replacement on a worker", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}},"required":["worker","path","old","new"]}`)}
	patch = provider.Tool{Name: "patch", Description: "Apply a patch in the apply_patch format on a worker", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"input":{"type":"string"}},"required":["worker","input"]}`)}
)

type args struct {
	Worker, Code, Interpreter, Path, Content, Old, New, Input string
}

// maxMedia bounds a file read as media. Anthropic's per-image limit, the
// tightest of the five envelopes.
const maxMedia = 5 << 20

// execute runs one tool on a worker. A failure is a result the model reads.
// A run's result also reports the lines the worker's side already dropped from
// its output; the file holding the whole is in the header. A read of a file
// whose sniffed type is in input is media; every other file is text.
func execute(ctx context.Context, w Worker, session, tool string, a args, input []string) (statefile.Record, int) {
	r := statefile.Record{Header: statefile.Header{Type: "text/plain"}}
	ok := func(body string) (statefile.Record, int) { r.Body = []byte(body); return r, 0 }
	fail := func(err error) (statefile.Record, int) { return ok("error: " + err.Error()) }
	switch tool {
	case "run":
		o, err := w.Run(ctx, session, a.Interpreter, a.Code)
		if err != nil {
			return fail(err)
		}
		r.Exit, r.FullOutput = &o.Exit, o.Full
		r.Body = o.Body
		return r, o.Cut
	case "read":
		b, err := w.Read(ctx, a.Path)
		if err != nil {
			return fail(err)
		}
		if t := http.DetectContentType(b); slices.Contains(input, t) {
			if len(b) > maxMedia {
				return fail(fmt.Errorf("%s is %d bytes of %s, over the %d byte media limit", a.Path, len(b), t, maxMedia))
			}
			r.Type, r.Body = t, b
			return r, 0
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
	if len(lines) <= worker.MaxLines && len(b) <= worker.MaxBytes {
		return b, 0
	}
	if tail {
		slices.Reverse(lines)
	}
	n, size := 0, 0
	for n < len(lines) && n < worker.MaxLines && size+len(lines[n])+1 <= worker.MaxBytes {
		size += len(lines[n]) + 1
		n++
	}
	if n == 0 {
		if tail {
			return b[len(b)-worker.MaxBytes:], len(lines)
		}
		return b[:worker.MaxBytes], len(lines)
	}
	kept := lines[:n]
	if tail {
		slices.Reverse(kept)
	}
	return bytes.Join(kept, []byte("\n")), len(lines) - n
}

// finish cuts a result over the limits and names where the whole is: a read's
// own file, or the file the worker spilled a run to. Run keeps its tail,
// where the error is; the rest keep their head. A non-zero exit is prefixed
// last, so the cut cannot take it. Media is whole or not at all.
func finish(tool string, a args, r statefile.Record, dropped int) statefile.Record {
	if r.Type != "text/plain" {
		return r
	}
	kept, cut := clip(r.Body, tool == "run")
	if cut += dropped; cut > 0 {
		if tool == "read" {
			r.FullOutput = a.Path
		}
		r.Truncated = true
		note := fmt.Sprintf("[%d lines cut, whole output at %s]", cut, r.FullOutput)
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
