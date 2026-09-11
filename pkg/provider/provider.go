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
	"fmt"
	"io"
	"net/http"
	"strings"

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

// Cache is the step record's cache header. Marks are the n of the records
// through which the prefix is cached: anthropic and bedrock put a breakpoint
// after the system prompt and after the last user block; openai and gemini
// cache every prefix, so the mark is the last record. TTL is seconds from the
// step's time: the provider's promise for anthropic and bedrock, a forecast
// for the rest. The next step's usage.cache_read is the truth.
type Cache struct {
	TTL   int   `json:"ttl"`
	Marks []int `json:"marks"`
}

func cacheHeader(ttl int, marks ...int) (json.RawMessage, error) {
	return marshal(Cache{TTL: ttl, Marks: marks})
}

// Envelope is one wire shape. Compile is deterministic and returns the cache
// markers it placed, which go on the step record. Stream calls emit once per
// block as the stream completes it and returns the step header with usage.
type Envelope interface {
	Compile(req Request) (body []byte, cache json.RawMessage, err error)
	Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error)
}

type Provider struct {
	Name     string // the provider tag on thinking and step records
	URL      string // full endpoint. gemini and bedrock take the model in the path, see Endpoint
	Key      string
	Envelope Envelope
	Headers  func(req *http.Request, key string) // auth and version headers
	Endpoint func(url, model string) string      // nil means URL as is
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
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return statefile.Header{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	p.Headers(hr, p.Key)
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		return statefile.Header{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return statefile.Header{}, fmt.Errorf("%s: %s: %s", p.Name, resp.Status, msg)
	}
	tagged := func(r statefile.Record) {
		if r.Kind == statefile.Thinking {
			r.Provider = p.Name
		}
		emit(r)
	}
	h, err := p.Envelope.Stream(resp.Body, tagged)
	if err != nil {
		return statefile.Header{}, err
	}
	sum := sha256.Sum256(body)
	h.Provider, h.Model, h.Request, h.Cache = p.Name, req.Model, "sha256:"+hex.EncodeToString(sum[:]), cache
	return h, nil
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
// ends its assistant turn and is kept for its cache markers.
type Turn struct {
	Assistant bool
	Records   []statefile.Record
	Step      *statefile.Record
}

func Turns(rest []statefile.Record) []Turn {
	var turns []Turn
	push := func(assistant bool, r statefile.Record) {
		if n := len(turns); n == 0 || turns[n-1].Assistant != assistant || turns[n-1].Step != nil {
			turns = append(turns, Turn{Assistant: assistant})
		}
		turns[len(turns)-1].Records = append(turns[len(turns)-1].Records, r)
	}
	for i := range rest {
		r := rest[i]
		switch r.Kind {
		case statefile.Text, statefile.Thinking, statefile.Call:
			push(true, r)
		case statefile.Step:
			if n := len(turns); n == 0 || !turns[n-1].Assistant || turns[n-1].Step != nil {
				turns = append(turns, Turn{Assistant: true})
			}
			turns[len(turns)-1].Step = &rest[i]
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
