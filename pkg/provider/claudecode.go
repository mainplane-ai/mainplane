package provider

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// The claude-code shape runs the user's Claude subscription through the
// unmodified claude binary, the one way Anthropic allows: one claude -p per
// step, which makes one request with its own login. The harness keeps the
// loop. The context goes in as stream-json frames, the tools as an MCP server
// that runs nothing, and a PreToolUse hook stops claude at the first tool
// call, so the call comes back to the harness unrun. The harness never sees
// the login: claude keeps it in the provider's dir, its CLAUDE_CONFIG_DIR.
//
// The request hash covers what the harness hands claude (model, effort,
// system prompt, tools, frames), not the wire bytes: claude adds its own
// system blocks and reminders, places its own cache marks, and names the
// tools mcp__mainplane__<name>.
//
// claude places its own cache marks. Its date reminder, with a mark, goes
// after the message that queries, so a replayed context would miss the cache
// from the first user turn on. So claude keeps its transcript in dir, a cache
// of the context and never its source: a step whose context up to its last
// assistant turn is exactly what a step before sent and got (same model,
// system prompt, tools and frames) resumes that step's transcript at that
// turn and sends only what came after it. Any other context is replayed whole.

// claudeVersion is the release the frame replay and the hook stop were tested
// on. Neither is a documented contract, so a new version is tested before the
// pin moves. The integrity values are npm's dist.integrity for
// @anthropic-ai/claude-code-<platform> at this version.
const claudeVersion = "2.1.307"

var claudeIntegrity = map[string]string{
	"darwin-arm64": "sha512-bBNAygs3y8j4jkwmBoq9PyYNHaYj4KsJ5Ca2dtj8CGeUAiFsTCOzzwNMbXHhMAhAkg7m0jXUo2S1YDQmYh1gUw==",
	"darwin-x64":   "sha512-sq02aE963Lywd7Wh2JPB7S/1pqlZ4c2dNJqKaFQR7p6BOIppsRoSbnUjFdfIQnTj7VvtUFRlKfJwLgaSS7KC8A==",
	"linux-arm64":  "sha512-llr4hI7rHHdCWeCgtl/QYIMIA/mpxcEauCqPQfyp83xUPvbSHDpccpglbrlChmgO52Qw8bQsAk27xylBwS7hHw==",
	"linux-x64":    "sha512-5KroAXOTbVYtrV86imf8ktsfVY2UgUrC6ws/FMblu+ny14kjFLxDBi2nHweJtUwSajA8yiLfOlNkwaKNbici8A==",
	"win32-arm64":  "sha512-lUNn2RMZQdbtiCkZbgieeTv71r85VTBwZ5f8jXMX5f9D9Iyhi4/k91Rtb7Pijtyy7EFDmvb4q8j4S1U3kxyjCQ==",
	"win32-x64":    "sha512-vCDHtsyHXelsub8+o6zvuvLiwfyuLqVrr0waO2mMGRsKaVpDVAfquMJLs5Odqx9W0HI6I6Cl3EeGBcg3GWNMiw==",
}

// claude names an MCP tool mcp__<server>__<tool>.
const claudePrefix = "mcp__mainplane__"

// claudeEnv keeps the step to one request with nothing of the machine's in
// it: no update, telemetry or memory, no CLAUDE.md, no compaction (an extra
// request; the harness owns the context limit), no token budget reminder. A
// description past claude's default cut would change our tools.
var claudeEnv = []string{
	"DISABLE_AUTOUPDATER=1",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
	"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
	"DISABLE_AUTO_COMPACT=1",
	"DISABLE_COMPACT=1",
	"CLAUDE_CODE_TOTAL_TOKENS_REMINDER=off",
	"CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH=100000",
}

// stopHook is the PreToolUse answer: deny the call and end claude's turn, so
// no tool runs and no second request goes out.
const stopHook = `{"continue":false,"stopReason":"mainplane runs tools","hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"mainplane runs tools"}}`

func ClaudeCode(dir string) Provider {
	return Provider{
		Envelope: claudeCode{},
		Run: func(ctx context.Context, body []byte, emit func(statefile.Record)) (statefile.Header, error) {
			return runClaude(ctx, dir, body, emit)
		},
	}
}

type claudeCode struct{}

func (claudeCode) Name() string { return "claude-code" }

func (claudeCode) Accepts(typ string) bool { return slices.Contains(images, typ) }

// Owns all but effort: claude builds the request and takes no other field.
func (claudeCode) Owns(field string) bool { return field != "effort" }

type ccReq struct {
	Model  string    `json:"model"`
	Effort string    `json:"effort,omitempty"`
	System string    `json:"system"`
	Tools  []mcpTool `json:"tools"`
	Frames []ccFrame `json:"frames"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ccFrame is one stream-json input line. A user frame with shouldQuery false
// joins the conversation without a request; only the last one queries.
type ccFrame struct {
	Type        string  `json:"type"`
	Message     anthMsg `json:"message"`
	Parent      *string `json:"parent_tool_use_id"`
	ShouldQuery *bool   `json:"shouldQuery,omitempty"`
}

func (e claudeCode) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	rest = fit(e.Name(), rest, anthLimits)
	body := ccReq{Model: req.Model, System: systemText(system), Tools: []mcpTool{}}
	if v, ok := req.Params["effort"]; ok {
		if err := json.Unmarshal(v, &body.Effort); err != nil {
			return nil, nil, fmt.Errorf("claude-code: effort is a string: %s", v)
		}
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, mcpTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	turns := Turns(rest)
	no := false
	for i, t := range turns {
		if t.Assistant {
			f, err := ccAssistant(t.Records)
			if err != nil {
				return nil, nil, err
			}
			body.Frames = append(body.Frames, f)
			continue
		}
		f := ccFrame{Type: "user", Message: anthMsg{Role: "user", Content: anthUser(t.Records)}}
		if i < len(turns)-1 {
			f.ShouldQuery = &no
		}
		body.Frames = append(body.Frames, f)
	}
	var marks []string
	if len(req.Context) > 0 {
		marks = append(marks, req.Context[len(req.Context)-1].ID)
	}
	cache, err := cacheHeader(0, marks...)
	if err != nil {
		return nil, nil, err
	}
	b, err := marshal(body)
	return b, cache, err
}

// ccAssistant is the frame of an assistant turn, with claude's tool names.
func ccAssistant(recs []statefile.Record) (ccFrame, error) {
	content, err := anthAssistant(claudeCode{}.Name(), recs)
	for j, c := range content {
		if b, ok := c.(anthBlock); ok && b.Type == "tool_use" {
			b.Name = claudePrefix + b.Name
			content[j] = b
		}
	}
	return ccFrame{Type: "assistant", Message: anthMsg{Role: "assistant", Content: content}}, err
}

// ccTurn is where a step's turn ends in claude's transcript: the session and
// the uuid of its last assistant message.
type ccTurn struct {
	Session string `json:"session"`
	UUID    string `json:"uuid"`
}

// turnKey names a context that ends with an assistant turn: the hash of the
// request without what claude does not keep in the transcript, effort and
// which frame queried.
func turnKey(req ccReq, frames []ccFrame) (string, error) {
	req.Effort, req.Frames = "", slices.Clone(frames)
	for i := range req.Frames {
		req.Frames[i].ShouldQuery = nil
	}
	// Through any and back, so key order is the same whether the frames
	// came from records or from the request body.
	b, err := marshal(req)
	if err != nil {
		return "", err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	if b, err = marshal(v); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// resumable is the turn to resume for frames and the frames after it, if a
// step before ended at exactly this context and claude still has its
// transcript. It claims the turn: another step from the same context, as in a
// second fork of the session, replays instead of resuming a transcript this
// step removes. A failed step gives the claim back with release.
func resumable(dir string, req ccReq) (turn ccTurn, rest []ccFrame, key string, err error) {
	k := len(req.Frames) - 1
	for k >= 0 && req.Frames[k].Type != "assistant" {
		k--
	}
	if k < 0 {
		return ccTurn{}, req.Frames, "", nil
	}
	if key, err = turnKey(req, req.Frames[:k+1]); err != nil {
		return ccTurn{}, nil, "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "turns", key))
	if errors.Is(err, fs.ErrNotExist) {
		return ccTurn{}, req.Frames, "", nil
	}
	if err != nil {
		return ccTurn{}, nil, "", err
	}
	if err := json.Unmarshal(b, &turn); err != nil {
		return ccTurn{}, nil, "", err
	}
	if t, _ := filepath.Glob(transcript(dir, turn.Session)); len(t) == 0 {
		return ccTurn{}, req.Frames, "", nil
	}
	if err := os.Remove(filepath.Join(dir, "turns", key)); errors.Is(err, fs.ErrNotExist) {
		return ccTurn{}, req.Frames, "", nil
	} else if err != nil {
		return ccTurn{}, nil, "", err
	}
	return turn, req.Frames[k+1:], key, nil
}

// release gives back the claim on a turn.
func release(dir string, turn ccTurn, key string) error {
	b, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "turns", key), b, 0o600)
}

// transcript is the glob of a session's transcript: claude keeps it under a
// directory named after its working directory.
func transcript(dir, session string) string {
	return filepath.Join(dir, "projects", "*", session+".jsonl")
}

// keepTurn notes where this step's turn ends, for the next step to resume,
// and drops the transcript it resumed: claude forked it into this one.
func keepTurn(dir string, req ccReq, recs []statefile.Record, end ccTurn, resumed ccTurn) error {
	f, err := ccAssistant(recs)
	if err != nil {
		return err
	}
	next, err := turnKey(req, append(slices.Clone(req.Frames), f))
	if err != nil {
		return err
	}
	b, err := json.Marshal(end)
	if err != nil {
		return err
	}
	turns := filepath.Join(dir, "turns")
	if err := os.MkdirAll(turns, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(turns, next), b, 0o600); err != nil {
		return err
	}
	if resumed.Session == "" {
		return nil
	}
	old, _ := filepath.Glob(transcript(dir, resumed.Session))
	for _, p := range old {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

// runClaude is one step: claude installed, the inert MCP server and the hook
// on a loopback port, the frames replayed, the stream read to its result.
func runClaude(ctx context.Context, dir string, body []byte, emit func(statefile.Record)) (_ statefile.Header, err error) {
	var req ccReq
	if err := json.Unmarshal(body, &req); err != nil {
		return statefile.Header{}, err
	}
	bin, err := installClaude(ctx, dir)
	if err != nil {
		return statefile.Header{}, err
	}
	resumed, frames, key, err := resumable(dir, req)
	if err != nil {
		return statefile.Header{}, err
	}
	if key != "" {
		defer func() {
			if err != nil {
				err = errors.Join(err, release(dir, resumed, key))
			}
		}()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return statefile.Header{}, err
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = http.Serve(ln, inert(req.Tools)) }()
	base := "http://" + ln.Addr().String()
	// claude puts its working directory in the request, so it stays the same
	// across steps for the cache.
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return statefile.Header{}, err
	}
	sys, err := os.CreateTemp(dir, "system-*.txt")
	if err != nil {
		return statefile.Header{}, err
	}
	defer func() { _ = os.Remove(sys.Name()) }()
	_, err = sys.WriteString(req.System)
	if cerr := sys.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return statefile.Header{}, err
	}
	mcp := fmt.Sprintf(`{"mcpServers":{"mainplane":{"type":"http","url":%q}}}`, base+"/mcp")
	settings := fmt.Sprintf(`{"hooks":{"PreToolUse":[{"matcher":"%s.*","hooks":[{"type":"http","url":%q,"onFailure":"block"}]}]}}`, claudePrefix, base+"/hook")
	args := []string{
		"-p", "--model", req.Model, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--system-prompt-file", sys.Name(), "--tools", "", "--strict-mcp-config", "--mcp-config", mcp, "--settings", settings,
		"--allowedTools", claudePrefix + "*", "--setting-sources", "", "--disable-slash-commands", "--max-turns", "1",
	}
	if resumed.Session != "" {
		args = append(args, "--resume", resumed.Session, "--resume-session-at", resumed.UUID, "--fork-session")
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = work, claudeEnviron(dir), 5*time.Second
	var stderr tail
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return statefile.Header{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return statefile.Header{}, err
	}
	if err := cmd.Start(); err != nil {
		return statefile.Header{}, err
	}
	timer := time.AfterFunc(idle, func() { cancel(fmt.Errorf("claude-code: no output for %s", idle)) })
	defer timer.Stop()
	out := bufio.NewReader(idleReader{stdout, timer})
	var recs []statefile.Record
	h, end, err := replay(stdin, out, frames, func(r statefile.Record) { recs = append(recs, r); emit(r) })
	if err != nil {
		cancel(err)
	}
	werr := cmd.Wait()
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, err) {
		return statefile.Header{}, cause
	}
	if err == nil && werr != nil {
		err = werr
	}
	if err != nil {
		return statefile.Header{}, fmt.Errorf("%w; claude stderr: %s", err, stderr.b.Bytes())
	}
	return h, keepTurn(dir, req, recs, end, resumed)
}

// replay sends the frames one by one, waiting for claude to take each
// shouldQuery false frame before the next, or they merge into the query;
// then it reads the step.
func replay(stdin io.WriteCloser, out *bufio.Reader, frames []ccFrame, emit func(statefile.Record)) (statefile.Header, ccTurn, error) {
	for _, f := range frames {
		b, err := marshal(f)
		if err != nil {
			return statefile.Header{}, ccTurn{}, err
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			return statefile.Header{}, ccTurn{}, err
		}
		if f.ShouldQuery == nil {
			continue
		}
		for {
			line, err := out.ReadBytes('\n')
			if err != nil {
				return statefile.Header{}, ccTurn{}, fmt.Errorf("claude-code: replay: %w", err)
			}
			var e ccEvent
			if json.Unmarshal(line, &e) == nil && e.Type == "result" {
				if e.IsError {
					return statefile.Header{}, ccTurn{}, fmt.Errorf("claude-code: replay: %s", line)
				}
				break
			}
		}
	}
	if err := stdin.Close(); err != nil {
		return statefile.Header{}, ccTurn{}, err
	}
	return claudeStream(out, emit)
}

type ccEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	UUID    string          `json:"uuid"`
	Session string          `json:"session_id"`
	IsError bool            `json:"is_error"`
	Result  string          `json:"result"`
	Stop    string          `json:"stop_reason"`
	Usage   anthUsage       `json:"usage"`
	Message json.RawMessage `json:"message"`
}

// Stream reads claude's stream-json: an assistant event per finished block,
// then the result with the request's usage. Blocks are kept as claude gave
// them.
func (claudeCode) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	h, _, err := claudeStream(resp, emit)
	return h, err
}

// claudeStream is Stream and where the turn ends in claude's transcript.
func claudeStream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, ccTurn, error) {
	var end ccTurn
	r := bufio.NewReader(resp)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) == 0 {
			if err != nil {
				return statefile.Header{}, ccTurn{}, fmt.Errorf("claude-code: stream ended before its result: %w", err)
			}
			continue
		}
		var e ccEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return statefile.Header{}, ccTurn{}, fmt.Errorf("claude-code: %w: %.200s", err, line)
		}
		switch e.Type {
		case "assistant":
			end.UUID = e.UUID
			var m struct {
				Content []json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(e.Message, &m); err != nil {
				return statefile.Header{}, ccTurn{}, fmt.Errorf("claude-code: %w: %.200s", err, line)
			}
			for _, raw := range m.Content {
				rec, ok, err := claudeBlock(raw)
				if err != nil {
					return statefile.Header{}, ccTurn{}, err
				}
				if ok {
					emit(rec)
				}
			}
		case "result":
			if e.IsError {
				return statefile.Header{}, ccTurn{}, fmt.Errorf("claude-code: %s: %s", e.Subtype, e.Result)
			}
			var usage statefile.Usage
			e.Usage.apply(&usage)
			end.Session = e.Session
			return statefile.Header{Usage: &usage, Stop: e.Stop}, end, nil
		}
	}
}

// claudeBlock is the record for one content block.
func claudeBlock(raw json.RawMessage) (statefile.Record, bool, error) {
	var b struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return statefile.Record{}, false, err
	}
	switch b.Type {
	case "text":
		if b.Text == "" {
			return statefile.Record{}, false, nil
		}
		return statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte(b.Text)}, true, nil
	case "thinking", "redacted_thinking":
		return statefile.Record{Header: statefile.Header{Kind: statefile.Thinking, Type: "application/json", Provider: "claude-code"}, Body: raw}, true, nil
	case "tool_use":
		name, ok := strings.CutPrefix(b.Name, claudePrefix)
		if !ok {
			return statefile.Record{}, false, fmt.Errorf("claude-code: claude called its own tool %q", b.Name)
		}
		r, err := callRecord(b.ID, name, string(b.Input))
		return r, err == nil, err
	}
	return statefile.Record{}, false, fmt.Errorf("claude-code: unknown block %.200s", raw)
}

// inert serves the tools as an MCP server (streamable HTTP, JSON answers)
// whose calls never run, and the PreToolUse hook that keeps claude from
// calling it.
func inert(tools []mcpTool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hook", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, stopHook)
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if in.ID == nil { // a notification
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch in.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": in.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mainplane", "version": claudeVersion}}
		case "tools/list":
			result = map[string]any{"tools": tools}
		case "tools/call":
			result = map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": "mainplane runs tools"}}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": result})
	})
	return mux
}

// claudeEnviron is the harness's environment without any Anthropic or Claude
// setting, which could swap the login for an API key or another config, plus
// the provider's config dir and claudeEnv.
func claudeEnviron(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k := strings.ToUpper(kv); !strings.HasPrefix(k, "ANTHROPIC_") && !strings.HasPrefix(k, "CLAUDE_") {
			env = append(env, kv)
		}
	}
	return append(append(env, "CLAUDE_CONFIG_DIR="+dir), claudeEnv...)
}

// tail keeps the last bytes claude wrote to stderr for the error.
type tail struct{ b bytes.Buffer }

func (t *tail) Write(p []byte) (int, error) {
	t.b.Write(p)
	if over := t.b.Len() - 4096; over > 0 {
		t.b.Next(over)
	}
	return len(p), nil
}

var installMu sync.Mutex

// installClaude puts the pinned claude in dir the first time a step needs
// it, from npm, checked against its integrity, and returns its path.
func installClaude(ctx context.Context, dir string) (string, error) {
	npm := func(s string) string { return cmp.Or(map[string]string{"windows": "win32", "amd64": "x64"}[s], s) }
	platform := npm(runtime.GOOS) + "-" + npm(runtime.GOARCH)
	want, ok := claudeIntegrity[platform]
	if !ok {
		return "", fmt.Errorf("claude-code: Claude Code has no build for %s", platform)
	}
	exe := "claude"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	bin := filepath.Join(dir, "claude-"+claudeVersion, exe)
	installMu.Lock()
	defer installMu.Unlock()
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	url := fmt.Sprintf("https://registry.npmjs.org/@anthropic-ai/claude-code-%s/-/claude-code-%s-%s.tgz", platform, platform, claudeVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude-code: %s: %s", url, resp.Status)
	}
	tgz, err := os.CreateTemp(dir, "claude-*.tgz")
	if err != nil {
		return "", err
	}
	defer func() { _ = tgz.Close(); _ = os.Remove(tgz.Name()) }()
	sum := sha512.New()
	if _, err := io.Copy(io.MultiWriter(tgz, sum), resp.Body); err != nil {
		return "", err
	}
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum.Sum(nil)); got != want {
		return "", fmt.Errorf("claude-code: %s has integrity %s, want %s", url, got, want)
	}
	if _, err := tgz.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	zr, err := gzip.NewReader(tgz)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("claude-code: %s has no package/%s", url, exe)
		}
		if err != nil {
			return "", err
		}
		if h.Name == "package/"+exe {
			break
		}
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		return "", err
	}
	part := bin + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, tr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return bin, os.Rename(part, bin)
}

// claudeLogin is Anthropic's own sign-in: claude auth login, with the
// provider's dir as its config dir, waits on a callback on localhost, and
// opens the URL that redirects there with $BROWSER, here a script that keeps
// it in a file. The person opens that URL and signs in on claude.ai. A
// browser on the harness's machine lands on claude itself; any other
// browser's page does not load, and its address, pasted, is handed to
// claude here. claude keeps the login in dir; the harness sees only the
// code, which works once.
func claudeLogin(ctx context.Context, dir string, show func(Prompt), paste <-chan string) error {
	bin, err := installClaude(ctx, dir)
	if err != nil {
		return err
	}
	script, body := filepath.Join(dir, "browser.sh"), "#!/bin/sh\nprintf '%s' \"$1\" > \"$0.url\"\n"
	if runtime.GOOS == "windows" {
		script, body = filepath.Join(dir, "browser.cmd"), "@echo %1> \"%~f0.url\"\r\n" // %1 keeps cmd's quotes, which the reader trims
	}
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return err
	}
	_ = os.Remove(script + ".url")
	cmd := exec.CommandContext(ctx, bin, "auth", "login", "--claudeai")
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, append(claudeEnviron(dir), "BROWSER="+script), 5*time.Second
	if _, err := cmd.StdinPipe(); err != nil { // open: at its end claude would stop waiting for a code
		return err
	}
	var out tail
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var opened []byte
	for len(opened) == 0 {
		select {
		case err := <-done:
			return fmt.Errorf("claude auth login ended before it opened its URL: %w: %s", err, out.b.Bytes())
		case <-time.After(100 * time.Millisecond):
		}
		opened, _ = os.ReadFile(script + ".url")
	}
	u, err := neturl.Parse(strings.Trim(strings.TrimSpace(string(opened)), `"`))
	if err != nil {
		return err
	}
	cb, err := neturl.Parse(u.Query().Get("redirect_uri"))
	if err != nil {
		return err
	}
	show(Prompt{Text: "Visit this URL in a browser and log in to Claude. Then paste the address it sends you to.", URL: u.String(), Paste: true})
	for {
		select {
		case text := <-paste:
			if err := claudeCallback(ctx, cb, text); err != nil {
				return err
			}
		case err := <-done:
			if err != nil {
				return fmt.Errorf("claude auth login: %w: %s", err, out.b.Bytes())
			}
			return nil
		}
	}
}

// claudeCallback hands claude's callback the address the browser landed on,
// as the browser would have.
func claudeCallback(ctx context.Context, cb *neturl.URL, pasted string) error {
	u, err := neturl.Parse(strings.TrimSpace(pasted))
	if err != nil || u.Host != cb.Host || u.Path != cb.Path || u.Query().Get("code") == "" {
		return fmt.Errorf("claude: the pasted text is not the address %s?code=...: start again", cb)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	c := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("claude: its callback: %w", err)
	}
	if err := r.Body.Close(); err != nil || r.StatusCode < 400 {
		return err
	}
	return fmt.Errorf("claude: its callback answered %s", r.Status)
}
