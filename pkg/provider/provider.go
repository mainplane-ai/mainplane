// Package provider turns a context of state file records into one LLM request
// and the streamed response back into records. Five envelopes exist. Same
// context, same envelope, same bytes: the step record's request hash proves it.
package provider

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for the arguments object
}

type Request struct {
	Model   string
	Key     string // session id, the routing key for prefix caches
	Tools   []Tool
	Context []statefile.Record // statefile.Build output
}

// Cache is the step record's cache header. Marks are the ids of the records
// through which the prefix is cached; ids because a chain spans files and n
// repeats across them. Anthropic and bedrock put a breakpoint after the
// system prompt and after the last user block; openai and gemini cache every
// prefix, so the mark is the last record. TTL is seconds from the step's
// time: the provider's promise for anthropic and bedrock, a forecast for the
// rest. The next step's usage.cache_read is the truth.
type Cache struct {
	TTL   int      `json:"ttl"`
	Marks []string `json:"marks"`
}

func cacheHeader(ttl int, marks ...string) (json.RawMessage, error) {
	return marshal(Cache{TTL: ttl, Marks: marks})
}

// Envelope is one wire shape. Name tags the thinking and step records it
// produces; only thinking with its own name is replayed. Compile is
// deterministic and returns the cache markers it placed, which go on the step
// record. Stream calls emit once per block as the stream completes it and
// returns the step header with usage.
type Envelope interface {
	Name() string
	Compile(req Request) (body []byte, cache json.RawMessage, err error)
	Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error)
}

type Provider struct {
	URL      string // full endpoint. gemini and bedrock take the model in the path, see Endpoint
	Key      string
	Envelope Envelope
	Headers  func(req *http.Request, key string) // auth and version headers
	Endpoint func(url, model string) string      // nil means URL as is
}

// Retry numbers are opencode's. A failure before the first block is out is
// retried: 429, 5xx, a network error, or a stream that errors before emitting.
// After a block is out the harness holds partial state, so that is terminal.
const (
	retries   = 5
	retryBase = 2 * time.Second // doubles per attempt, plus up to 25% jitter
	retryCap  = 30 * time.Second
)

// httpError is a non-200 response. After is the Retry-After header, 0 if none.
type httpError struct {
	name, status string
	code         int
	body         []byte
	after        time.Duration
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: %s: %s", e.name, e.status, e.body) }

func transient(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.code == http.StatusTooManyRequests || he.code >= 500
	}
	return true // network or early stream failure
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
		h, emitted, err := p.once(ctx, url, body, emit)
		if err == nil {
			sum := sha256.Sum256(body)
			h.Provider, h.Model, h.Request, h.Cache = p.Envelope.Name(), req.Model, "sha256:"+hex.EncodeToString(sum[:]), cache
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
func (p Provider) once(ctx context.Context, url string, body []byte, emit func(statefile.Record)) (h statefile.Header, emitted bool, err error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return h, false, err
	}
	hr.Header.Set("Content-Type", "application/json")
	p.Headers(hr, p.Key)
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return h, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	name := p.Envelope.Name()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return h, false, &httpError{name: name, status: resp.Status, code: resp.StatusCode, body: msg, after: retryAfter(resp.Header)}
	}
	tagged := func(r statefile.Record) {
		emitted = true
		if r.Kind == statefile.Thinking {
			r.Provider = name
		}
		emit(r)
	}
	h, err = p.Envelope.Stream(resp.Body, tagged)
	return h, emitted, err
}

// callRecord builds a call record. Providers stream nothing for a tool called
// without arguments; the body always holds an object.
func callRecord(id, name, args string) (statefile.Record, error) {
	if args == "" {
		args = "{}"
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
// errors; assistant turns hold the blocks one step closed. The step record
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
		case statefile.Result, statefile.Message, statefile.System, statefile.Summary, statefile.Error:
			push(false, r)
		case statefile.Start, statefile.Link, statefile.Config:
		}
	}
	return turns
}

// Call is the body of a call record.
type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
