// Package provider turns a context of state file records into one LLM request
// and the streamed response back into records. Five envelopes exist. Same
// context, same envelope, same bytes: the step record's request hash proves it.
package provider

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	neturl "net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Shapes is every wire format a provider in the config may have, each built
// from the provider's config. The url is the base url the vendor's official
// SDK takes; the shape appends the API path. An empty url is the vendor's own.
var Shapes = map[string]func(c Config) Provider{
	"anthropic":   func(c Config) Provider { return Anthropic(c.URL, c.Key) },
	"openai":      func(c Config) Provider { return OpenAI(c.URL, c.Key) },
	"openai-chat": func(c Config) Provider { return OpenAIChat(c.URL, c.Key) },
	"google":      func(c Config) Provider { return Gemini(c.URL, c.Key) },
	"bedrock":     func(c Config) Provider { return Bedrock(c.URL, c.Region, c.Key) },
	"chatgpt":     func(c Config) Provider { return ChatGPT(c.URL, c.Dir) },
}

// Config is a provider as the harness config has it. Dir is the harness's
// own directory for this provider, where a login keeps what it needs.
type Config struct {
	Key, URL, Region, Dir string
}

// Logins are the shapes whose user signs in instead of pasting a key. A login
// shows the user prompts, reads what the user pastes, and returns nil once
// what the provider needs is in dir.
var Logins = map[string]func(ctx context.Context, dir string, show func(Prompt), paste <-chan string) error{
	"chatgpt": chatgptLogin,
}

// Prompt is what a login asks of the user: open URL, enter Code there if one
// is given, and paste back what Text says if Paste.
type Prompt struct {
	Text  string `json:"text"`
	URL   string `json:"url"`
	Code  string `json:"code,omitempty"`
	Paste bool   `json:"paste,omitempty"`
}

// LoginState is a login as the harness's routes show it.
type LoginState struct {
	State string `json:"state"` // pending, done or failed
	Error string `json:"error,omitempty"`
	Prompt
}

// base is the config's url, else the vendor's own, without a trailing slash,
// for the shape to append its path to.
func base(url, vendor string) string { return strings.TrimSuffix(cmp.Or(url, vendor), "/") }

// normalStops are the stops each envelope names when the model ended its turn
// itself. Any other stop on a step with no call is the output limit, a
// refusal, or a filter, and the reply may be cut short or missing.
// openai-chat compatible servers may send no finish_reason.
var normalStops = map[string][]string{
	"anthropic":   {"end_turn", "stop_sequence"},
	"bedrock":     {"end_turn", "stop_sequence"},
	"openai":      {"completed"},
	"openai-chat": {"stop", ""},
	"google":      {"STOP"},
}

// Normal reports whether a step ended because the model ended its turn.
func Normal(step statefile.Header) bool {
	return slices.Contains(normalStops[step.Provider], step.Stop)
}

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for the arguments object
}

// Params are the session's vendor fields, see encode.
type Request struct {
	Model   string
	Key     string // record 1's id, the routing key for prefix caches: a copy shares it with the session it copies, as it shares the prefix
	Tools   []Tool
	Context []statefile.Record // statefile.Build output
	Params  map[string]json.RawMessage
}

// Cache is the step record's cache header, facts only. Marks are the ids of
// the records that end a cache breakpoint. Anthropic and bedrock put one after
// the system prompt and one after the last user block; openai and gemini place
// their own, so the mark is the last record sent. TTL is seconds from the
// step's sent time, set only where the request the harness sends fixes it:
// anthropic and bedrock. The step's usage says what was read and written.
type Cache struct {
	TTL   int      `json:"ttl,omitempty"`
	Marks []string `json:"marks"`
}

func cacheHeader(ttl int, marks ...string) (json.RawMessage, error) {
	return marshal(Cache{TTL: ttl, Marks: marks})
}

// Envelope is one wire shape. Name tags the thinking and step records it
// produces; only thinking with its own name is replayed. Compile is
// deterministic and returns the cache markers it placed, which go on the step
// record. Stream calls emit once per block as the stream completes it and
// returns the step header with usage. Accepts is the media types the envelope
// encodes as media; a record of any other type goes as text. Owns is the body
// fields Compile builds from the session, which params may not set.
type Envelope interface {
	Name() string
	Accepts(typ string) bool
	Owns(field string) bool
	Compile(req Request) (body []byte, cache json.RawMessage, err error)
	Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error)
}

// images are the types every envelope takes as an image block.
var images = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

type Provider struct {
	URL      string // full endpoint. gemini and bedrock take the model in the path, see Endpoint
	Key      string
	Envelope Envelope
	Headers  func(req *http.Request, key string)       // auth and version headers
	Endpoint func(url, model string) string            // nil means URL as is
	Token    func(ctx context.Context) (string, error) // the key of each request, for a login's key that expires; nil means Key
	Session  string                                    // the header that carries Request.Key, where the vendor routes its cache on a header
}

// Retry numbers are opencode's. A failure before the first block is out is
// retried: 429, 5xx, a network error, or a stream that errors before emitting.
// After a block is out the harness holds partial state, so that is terminal.
const (
	retries   = 5
	retryBase = 2 * time.Second // doubles per attempt, plus up to 25% jitter
	retryCap  = 30 * time.Second
)

// idle bounds the wait for response headers and then the gap between body
// bytes, which a silent connection otherwise holds forever. 300 s is
// opencode's header and chunk timeout, pi's, and codex's per event: a
// reasoning model can think that long before it sends a byte. It counts bytes,
// not events, so bedrock's binary frames are bounded too.
const idle = 300 * time.Second

// httpError is a non-200 response. After is the Retry-After header, 0 if none.
type httpError struct {
	name, status string
	code         int
	body         []byte
	after        time.Duration
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: %s: %s", e.name, e.status, e.body) }

// configError is a failure no retry mends, such as a provider not signed in.
type configError struct{ error }

func transient(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.code == http.StatusTooManyRequests || he.code >= 500
	}
	return !errors.As(err, new(configError)) // network or early stream failure
}

func backoff(attempt int, err error) time.Duration {
	var he *httpError
	if errors.As(err, &he) && he.after > 0 {
		return he.after
	}
	d := retryBase << attempt
	return min(d+time.Duration(rand.Int64N(int64(d/4))), retryCap)
}

func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// Step is one LLM call. Blocks are emitted as they complete; the returned
// header is the step record minus kind, id, and upto.
func (p Provider) Step(ctx context.Context, req Request, emit func(statefile.Record)) (statefile.Header, error) {
	body, cache, err := p.Envelope.Compile(req)
	if err != nil {
		return statefile.Header{}, err
	}
	url := p.URL
	if p.Endpoint != nil {
		url = p.Endpoint(p.URL, req.Model)
	}
	if u, err := neturl.Parse(url); err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return statefile.Header{}, fmt.Errorf("bad url %q", url) // configuration, not transient: checked before the retry loop
	}
	for attempt := 0; ; attempt++ {
		sent := time.Now().UTC().Truncate(time.Millisecond)
		h, emitted, err := p.once(ctx, url, req.Key, body, emit)
		if err == nil {
			sum := sha256.Sum256(body)
			h.Provider, h.Model, h.Request, h.Cache, h.Sent = p.Envelope.Name(), req.Model, "sha256:"+hex.EncodeToString(sum[:]), cache, sent
			return h, nil
		}
		if emitted || attempt == retries || !transient(err) {
			return statefile.Header{}, err
		}
		select {
		case <-time.After(backoff(attempt, err)):
		case <-ctx.Done():
			return statefile.Header{}, ctx.Err()
		}
	}
}

// once is one HTTP attempt. emitted reports whether any block reached emit.
// An idle timeout cancels the request, and its cause is the error.
func (p Provider) once(ctx context.Context, url, session string, body []byte, emit func(statefile.Record)) (h statefile.Header, emitted bool, err error) {
	name := p.Envelope.Name()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(idle, func() { cancel(fmt.Errorf("%s: no bytes for %s", name, idle)) })
	defer timer.Stop()
	defer func() {
		if cause := context.Cause(ctx); err != nil && cause != nil {
			err = cause
		}
	}()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return h, false, err
	}
	hr.Header.Set("Content-Type", "application/json")
	key := p.Key
	if p.Token != nil {
		if key, err = p.Token(ctx); err != nil {
			return h, false, err
		}
	}
	p.Headers(hr, key)
	if p.Session != "" {
		hr.Header.Set(p.Session, session)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return h, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	timer.Reset(idle)
	rb := idleReader{resp.Body, timer}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(rb, 4096))
		return h, false, &httpError{name: name, status: resp.Status, code: resp.StatusCode, body: msg, after: retryAfter(resp.Header)}
	}
	tagged := func(r statefile.Record) {
		emitted = true
		if r.Kind == statefile.Thinking {
			r.Provider = name
		}
		emit(r)
	}
	h, err = p.Envelope.Stream(rb, tagged)
	return h, emitted, err
}

// idleReader restarts the idle timer on every read that returns bytes.
type idleReader struct {
	r io.Reader
	t *time.Timer
}

func (b idleReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.t.Reset(idle)
	}
	return n, err
}

// callRecord builds a call record. Providers stream nothing for a tool called
// without arguments; the body always holds an object. Arguments that are not
// JSON were cut off, most often by the output token limit.
func callRecord(id, name, args string) (statefile.Record, error) {
	if args == "" {
		args = "{}"
	}
	if !json.Valid([]byte(args)) {
		return statefile.Record{}, fmt.Errorf("call %s: arguments are not complete JSON, likely cut at the output token limit: %.200s", name, args)
	}
	body, err := marshal(Call{Name: name, Arguments: json.RawMessage(args)})
	return statefile.Record{Header: statefile.Header{ID: id, Kind: statefile.Call, Type: "application/json"}, Body: body}, err
}

// systemText joins the leading system records into the system prompt.
func systemText(system []statefile.Record) string {
	parts := make([]string, len(system))
	for i, r := range system {
		parts[i] = string(r.Body)
	}
	return strings.Join(parts, "\n\n")
}

// marshal is json.Marshal without HTML escaping, so text and arguments keep
// the bytes the model emitted.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// encode marshals a request body and applies params to it as a JSON merge
// patch (RFC 7396) in the vendor's own field names: an object merges into the
// object it names, null deletes, anything else replaces. The harness does not
// know every vendor's fields, so the vendor judges them and its 400 is the
// error. The harness refuses at create only the fields the envelope Owns.
func encode(body any, params map[string]json.RawMessage) ([]byte, error) {
	b, err := marshal(body)
	if err != nil || len(params) == 0 {
		return b, err
	}
	return merge(b, params)
}

func merge(target json.RawMessage, patch map[string]json.RawMessage) (json.RawMessage, error) {
	var t map[string]json.RawMessage
	if json.Unmarshal(target, &t) != nil || t == nil {
		t = map[string]json.RawMessage{} // RFC 7396: a target that is not an object becomes one
	}
	for k, v := range patch {
		var sub map[string]json.RawMessage
		switch {
		case string(v) == "null":
			delete(t, k)
		case json.Unmarshal(v, &sub) == nil:
			m, err := merge(t[k], sub)
			if err != nil {
				return nil, err
			}
			t[k] = m
		default:
			t[k] = v
		}
	}
	return marshal(t)
}

// noted is a result's text as the model reads it: the text, then its note on
// the next line.
func noted(text, note string) string {
	if note == "" {
		return text
	}
	if text == "" {
		return note
	}
	return strings.TrimSuffix(text, "\n") + "\n" + note
}

// Split separates the leading system records, which are the system prompt,
// from the rest of the context.
func Split(ctx []statefile.Record) (system, rest []statefile.Record) {
	for i, r := range ctx {
		if r.Kind != statefile.System {
			return ctx[:i], ctx[i:]
		}
	}
	return ctx, nil
}

// Turns groups the non-system context into alternating turns in API order:
// user turns hold results, messages, later system records, summaries, and
// errors; assistant turns hold the blocks one step closed. A message begins
// with a line naming its via, so the model knows who sent it; media (image,
// pdf, audio, video) gets that line as a text record before it. The step record
// ends its assistant turn. A step that closed no blocks, a model that said
// nothing, is no turn at all: every provider rejects an empty assistant turn,
// and every provider merges the user turns around it.
type Turn struct {
	Assistant bool
	Records   []statefile.Record
	closed    bool
}

func Turns(rest []statefile.Record) []Turn {
	var turns []Turn
	push := func(assistant bool, r statefile.Record) {
		if n := len(turns); n == 0 || turns[n-1].Assistant != assistant || turns[n-1].closed {
			turns = append(turns, Turn{Assistant: assistant})
		}
		turns[len(turns)-1].Records = append(turns[len(turns)-1].Records, r)
	}
	for _, r := range rest {
		switch r.Kind {
		case statefile.Text, statefile.Thinking, statefile.Call:
			push(true, r)
		case statefile.Step:
			if n := len(turns); n > 0 && turns[n-1].Assistant {
				turns[n-1].closed = true
			}
		case statefile.Message:
			line := "via " + r.Via + "\n"
			if !strings.HasPrefix(r.Type, "text/") {
				push(false, statefile.Record{Header: statefile.Header{Kind: statefile.Message, Type: "text/plain"}, Body: []byte(line)})
			} else {
				r.Body = append([]byte(line), r.Body...)
			}
			push(false, r)
		case statefile.Result, statefile.System, statefile.Error:
			push(false, r)
		case statefile.Start, statefile.Config, statefile.Name:
		}
	}
	return turns
}

// Call is the body of a call record.
type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
