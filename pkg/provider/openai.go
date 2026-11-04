package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// respLimits: the Responses API rejects an image of over 30000 patches of
// 32x32 px, counted with each side rounded up (5537x5537 is 30276), and more
// than 1500 images. 28 M pixels stays under the patch limit at any aspect up
// to the 65535 px side. A 60 MB image passed; the docs say 512 MB a request.
// chatgpt uses this envelope.
var respLimits = Limits{Side: 65535, Pixels: 28_000_000, Bytes: 50_000_000, Count: 1500, Request: 300_000_000}

func OpenAI(url, key string) Provider {
	return Provider{
		URL:      base(url, openaiURL) + "/responses",
		Key:      key,
		Envelope: respCache[base(url, openaiURL)],
		Headers:  bearer,
		Socket:   base(url, openaiURL) == openaiURL,
	}
}

// respCache is the cache of each vendor behind the Responses shape, by base
// url, measured. Each keeps a prefix by prompt_cache_key. OpenAI keeps it 30
// minutes from the start of the last request that read or wrote it; 12 hours
// costs 2.5x a write instead of 1.25x, which replayed over a month of real
// sessions cost 12% more. Meta evicts by load before a hard 30 minutes from
// the start. xAI evicts by load: grok-4.20 reads 75% of the time to 20
// minutes and rarely past 25, grok-4.7 to 10 minutes and rarely past 20 (see
// grok47TTL); clock not measured. ChatGPT's is its own, below.
var respCache = map[string]openai{
	openaiURL:                {ttl: 1800, anchor: "start", options: true},
	"https://api.meta.ai/v1": {ttl: 1800, anchor: "start"},
	"https://api.x.ai/v1":    {ttl: 1200},
}

const grok47TTL = 600

// openaiURL is OpenAI's own API, the one endpoint known to speak Responses
// over a WebSocket. Other vendors take the openai shape with their own url.
const openaiURL = "https://api.openai.com/v1"

func bearer(r *http.Request, key string) { r.Header.Set("Authorization", "Bearer "+key) }

// openai is the Responses envelope. ttl and anchor are its vendor's cache, as
// Cache has them. options sends prompt_cache_options, which only OpenAI's own
// API takes: ChatGPT answers 400, and Meta and xAI ignore it.
type openai struct {
	ttl     int
	anchor  string
	options bool
}

// respOptions makes OpenAI report cache writes by TTL in usage; the TTL is
// its default.
var respOptions = &struct {
	Mode string `json:"mode"`
	TTL  string `json:"ttl"`
}{"implicit", "30m"}

func (openai) Name() string { return "openai" }

func (openai) Accepts(typ string) bool { return slices.Contains(images, typ) }

func (openai) Owns(field string) bool {
	return slices.Contains([]string{"model", "store", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention", "stream", "include", "instructions", "tools", "input"}, field)
}

type respReq struct {
	Model        string     `json:"model"`
	Store        bool       `json:"store"`
	CacheKey     string     `json:"prompt_cache_key,omitempty"`
	CacheOptions any        `json:"prompt_cache_options,omitempty"`
	Stream       bool       `json:"stream"`
	Include      []string   `json:"include"`
	Instructions string     `json:"instructions,omitempty"`
	Tools        []respTool `json:"tools,omitempty"`
	Input        []any      `json:"input"`
}

type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type respPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type respMessage struct {
	Role    string     `json:"role"`
	Content []respPart `json:"content"`
}

type respCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// respOutput output is a string for text results and []respPart for images.
type respOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output any    `json:"output"`
}

func (e openai) Compile(req Request) ([]byte, Cache, error) {
	system, rest := Split(req.Context)
	rest = fit(e.Name(), rest, respLimits)
	body := respReq{
		Model:        req.Model,
		CacheKey:     req.Key,
		Stream:       true,
		Include:      []string{"reasoning.encrypted_content"},
		Instructions: systemText(system),
		Input:        []any{},
	}
	if e.options {
		body.CacheOptions = respOptions
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, respTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	for _, t := range Turns(rest) {
		if !t.Assistant {
			body.Input = append(body.Input, respUser(t.Records)...)
			continue
		}
		items, err := e.assistant(t.Records)
		if err != nil {
			return nil, Cache{}, err
		}
		body.Input = append(body.Input, items...)
	}
	b, err := encode(body, req.Params)
	ttl := e.ttl
	if strings.HasPrefix(req.Model, "grok-4.7") {
		ttl = grok47TTL
	}
	return b, Cache{TTL: ttl, Anchor: e.anchor, Marks: []string{req.Context[len(req.Context)-1].ID}}, err
}

func imageURL(r statefile.Record) string {
	return "data:" + r.Type + ";base64," + base64.StdEncoding.EncodeToString(r.Body)
}

func respUser(recs []statefile.Record) []any {
	var items []any
	var parts []respPart
	for _, r := range recs {
		switch r.Kind {
		case statefile.Result:
			out := respOutput{Type: "function_call_output", CallID: r.For, Output: noted(string(r.Body), r.Note)}
			if slices.Contains(images, r.Type) {
				parts := []respPart{{Type: "input_image", ImageURL: imageURL(r), Detail: "auto"}}
				if r.Note != "" {
					parts = append(parts, respPart{Type: "input_text", Text: r.Note})
				}
				out.Output = parts
			}
			items = append(items, out)
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(images, r.Type) {
				parts = append(parts, respPart{Type: "input_image", ImageURL: imageURL(r), Detail: "auto"})
			} else {
				parts = append(parts, respPart{Type: "input_text", Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Name, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	if len(parts) > 0 {
		items = append(items, respMessage{Role: "user", Content: parts})
	}
	return items
}

func (e openai) assistant(recs []statefile.Record) ([]any, error) {
	var items []any
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			items = append(items, respMessage{Role: "assistant", Content: []respPart{{Type: "output_text", Text: string(r.Body)}}})
		case statefile.Thinking:
			if r.Provider == e.Name() {
				items = append(items, json.RawMessage(r.Body))
			}
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return nil, fmt.Errorf("call %s: %w", r.ID, err)
			}
			items = append(items, respCall{Type: "function_call", CallID: r.ID, Name: c.Name, Arguments: string(c.Arguments)})
		case statefile.Start, statefile.Config, statefile.Name, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Error:
		}
	}
	return items, nil
}

type respEvent struct {
	Type     string          `json:"type"`
	Item     json.RawMessage `json:"item"`
	Response *respResponse   `json:"response"`
}

type respResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage json.RawMessage `json:"usage"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
}

type respItem struct {
	Type      string     `json:"type"`
	CallID    string     `json:"call_id"`
	Name      string     `json:"name"`
	Arguments string     `json:"arguments"`
	Content   []respPart `json:"content"`
}

func respRecord(raw json.RawMessage) (statefile.Record, bool, error) {
	var item respItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return statefile.Record{}, false, err
	}
	var r statefile.Record
	switch item.Type {
	case "message":
		for _, p := range item.Content {
			r.Body = append(r.Body, p.Text...)
		}
		if len(r.Body) == 0 {
			return r, false, nil
		}
		r.Kind, r.Type = statefile.Text, "text/plain"
	case "reasoning":
		r.Kind, r.Type, r.Body = statefile.Thinking, "application/json", raw
	case "function_call":
		r, err := callRecord(item.CallID, item.Name, item.Arguments)
		return r, err == nil, err
	default:
		return r, false, fmt.Errorf("openai: unknown output item %q", item.Type)
	}
	return r, true, nil
}

func (openai) Stream(resp io.Reader, _ *Cache, emit func(statefile.Record)) (statefile.Header, error) {
	var s respStream
	err := Events(resp, func(_, data string) error {
		if data == "[DONE]" { // Meta ends its stream with Chat Completions' end marker
			return nil
		}
		return s.event([]byte(data), emit)
	})
	if err == nil && !s.done {
		err = fmt.Errorf("openai: stream ended before response.completed")
	}
	return s.header(), err
}

// respStream folds the Responses API's events into records and the step
// header. Server-sent events and the WebSocket carry the same events.
type respStream struct {
	usage    statefile.Usage
	stop, id string // id is the response's, which a WebSocket step chains from
	done     bool
}

func (s *respStream) event(data []byte, emit func(statefile.Record)) error {
	var e respEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	switch e.Type {
	case "error", "response.failed":
		return fmt.Errorf("openai: %s", data)
	case "response.output_item.done":
		r, ok, err := respRecord(e.Item)
		if err != nil {
			return err
		}
		if ok {
			emit(r)
		}
	case "response.completed", "response.incomplete":
		if e.Response == nil || e.Response.Usage == nil {
			return fmt.Errorf("openai: %s without usage: %.200s", e.Type, data)
		}
		s.done, s.id, s.stop = true, e.Response.ID, e.Response.Status
		if d := e.Response.IncompleteDetails; d != nil {
			s.stop = d.Reason
		}
		var u respUsage
		if err := json.Unmarshal(e.Response.Usage, &u); err != nil {
			return fmt.Errorf("openai: usage: %w", err)
		}
		s.usage = statefile.Usage{
			Input:      u.InputTokens - u.InputTokensDetails.CachedTokens - u.InputTokensDetails.CacheWriteTokens,
			Output:     u.OutputTokens,
			CacheRead:  u.InputTokensDetails.CachedTokens,
			CacheWrite: u.InputTokensDetails.CacheWriteTokens,
			Vendor:     e.Response.Usage, // OpenAI's cache_write_tokens_details: the writes by TTL
		}
	}
	return nil
}

func (s *respStream) header() statefile.Header {
	return statefile.Header{Usage: &s.usage, Stop: s.stop}
}
