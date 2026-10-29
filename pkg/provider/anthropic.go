package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// anthLimits: Claude scales an image down to a 2576 px side and about 3.7 M
// pixels itself (1568 px and 1.15 M on 4.5 models), so a larger one shows the
// model nothing more; token counts stop growing there. That also keeps every
// image under the 3000 px the Messages API allows once a request has over 20
// images (8000 px below that). Measured past that: an image over 10 MiB of
// base64, more than 100 images on 200k context models (600 on others), a body
// near 128 MiB (the docs say 32 MB). claude -p sends to the same API.
var anthLimits = Limits{Side: 2576, Pixels: 3_700_000, Bytes: 10 << 20 * 3 / 4, Count: 100, Request: 80_000_000}

// The body carries only what the session builds. max_tokens, which the
// Messages API requires, thinking, and effort come from params: the API, not
// the harness, says what each model takes.
const anthCacheTTL = 300 // ephemeral: 5 minutes from the start of the request that writes or reads it

func Anthropic(url, key string) Provider {
	return Provider{
		URL:      base(url, "https://api.anthropic.com") + "/v1/messages",
		Key:      key,
		Envelope: anthropic{},
		Headers: func(r *http.Request, key string) {
			r.Header.Set("x-api-key", key)
			r.Header.Set("anthropic-version", "2023-06-01")
		},
	}
}

type anthropic struct{}

func (anthropic) Name() string { return "anthropic" }

func (anthropic) Accepts(typ string) bool { return slices.Contains(images, typ) }

func (anthropic) Owns(field string) bool {
	return slices.Contains([]string{"model", "stream", "system", "tools", "messages"}, field)
}

type anthReq struct {
	Model    string      `json:"model"`
	Stream   bool        `json:"stream"`
	System   []anthBlock `json:"system,omitempty"`
	Tools    []anthTool  `json:"tools,omitempty"`
	Messages []anthMsg   `json:"messages"`
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthMsg content is []anthBlock for user turns and []any for assistant turns,
// where thinking blocks are the stored bytes replayed as is.
type anthMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthCache struct {
	Type string `json:"type"`
}

type anthSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      []anthBlock     `json:"content,omitempty"`
	Source       *anthSource     `json:"source,omitempty"`
	CacheControl *anthCache      `json:"cache_control,omitempty"`
}

var ephemeral = &anthCache{Type: "ephemeral"}

func (e anthropic) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	rest = fit(e.Name(), rest, anthLimits)
	body := anthReq{Model: req.Model, Stream: true, Messages: []anthMsg{}}
	var marks []string
	if len(system) > 0 {
		body.System = []anthBlock{{Type: "text", Text: systemText(system), CacheControl: ephemeral}}
		marks = append(marks, system[len(system)-1].ID)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	turns := Turns(rest)
	for i, t := range turns {
		if t.Assistant {
			content, err := anthAssistant(e.Name(), t.Records)
			if err != nil {
				return nil, nil, err
			}
			body.Messages = append(body.Messages, anthMsg{Role: "assistant", Content: content})
			continue
		}
		blocks := anthUser(t.Records)
		if i == len(turns)-1 {
			blocks[len(blocks)-1].CacheControl = ephemeral
			marks = append(marks, t.Records[len(t.Records)-1].ID)
		}
		body.Messages = append(body.Messages, anthMsg{Role: "user", Content: blocks})
	}
	cache, err := cacheHeader(anthCacheTTL, marks...)
	if err != nil {
		return nil, nil, err
	}
	b, err := encode(body, req.Params)
	return b, cache, err
}

func anthUser(recs []statefile.Record) []anthBlock {
	var blocks []anthBlock
	for _, r := range recs {
		switch r.Kind {
		case statefile.Result:
			b := anthBlock{Type: "tool_result", ToolUseID: r.For}
			switch {
			case slices.Contains(images, r.Type):
				b.Content = []anthBlock{{Type: "image", Source: &anthSource{
					Type: "base64", MediaType: r.Type, Data: base64.StdEncoding.EncodeToString(r.Body),
				}}}
				if r.Note != "" {
					b.Content = append(b.Content, anthBlock{Type: "text", Text: r.Note})
				}
			case len(r.Body) > 0 || r.Note != "":
				b.Content = []anthBlock{{Type: "text", Text: noted(string(r.Body), r.Note)}}
			}
			blocks = append(blocks, b)
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(images, r.Type) {
				blocks = append(blocks, anthBlock{Type: "image", Source: &anthSource{Type: "base64", MediaType: r.Type, Data: base64.StdEncoding.EncodeToString(r.Body)}})
			} else {
				blocks = append(blocks, anthBlock{Type: "text", Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Name, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	return blocks
}

// anthAssistant is an assistant turn's content. Only thinking that name made
// is replayed.
func anthAssistant(name string, recs []statefile.Record) ([]any, error) {
	var content []any
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			content = append(content, anthBlock{Type: "text", Text: string(r.Body)})
		case statefile.Thinking:
			if r.Provider == name {
				content = append(content, json.RawMessage(r.Body))
			}
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return nil, fmt.Errorf("call %s: %w", r.ID, err)
			}
			content = append(content, anthBlock{Type: "tool_use", ID: r.ID, Name: c.Name, Input: c.Arguments})
		case statefile.Start, statefile.Config, statefile.Name, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Error:
		}
	}
	return content, nil
}

type anthEvent struct {
	Type         string     `json:"type"`
	Index        int        `json:"index"`
	Message      anthStart  `json:"message"`
	ContentBlock *anthPart  `json:"content_block"`
	Delta        *anthDelta `json:"delta"`
	Usage        *anthUsage `json:"usage"`
}

type anthStart struct {
	Usage anthUsage `json:"usage"`
}

// anthPart is a content block under assembly. Deltas append to the fields.
type anthPart struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Data      string `json:"data"`
	args      string
}

type anthDelta struct {
	Type        string `json:"type"`
	StopReason  string `json:"stop_reason"` // message_delta
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	PartialJSON string `json:"partial_json"`
}

type anthUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

func (u anthUsage) apply(to *statefile.Usage) {
	set := func(dst *int, src *int) {
		if src != nil {
			*dst = *src
		}
	}
	set(&to.Input, u.InputTokens)
	set(&to.Output, u.OutputTokens)
	set(&to.CacheRead, u.CacheReadInputTokens)
	set(&to.CacheWrite, u.CacheCreationInputTokens)
}

func (p *anthPart) add(d anthDelta) {
	p.Text += d.Text
	p.Thinking += d.Thinking
	p.Signature += d.Signature
	p.args += d.PartialJSON
}

// record turns a finished block into its record. Empty text blocks yield none.
func (p *anthPart) record() (statefile.Record, bool, error) {
	var r statefile.Record
	var err error
	switch p.Type {
	case "text":
		if p.Text == "" {
			return r, false, nil
		}
		r.Kind, r.Type, r.Body = statefile.Text, "text/plain", []byte(p.Text)
	case "thinking":
		r.Kind, r.Type = statefile.Thinking, "application/json"
		r.Body, err = marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{p.Type, p.Thinking, p.Signature})
	case "redacted_thinking":
		r.Kind, r.Type = statefile.Thinking, "application/json"
		r.Body, err = marshal(struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}{p.Type, p.Data})
	case "tool_use":
		r, err = callRecord(p.ID, p.Name, p.args)
	default:
		return r, false, fmt.Errorf("anthropic: unknown block %q", p.Type)
	}
	return r, err == nil, err
}

func (anthropic) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	parts := map[int]*anthPart{}
	var usage statefile.Usage
	var stop string
	var done bool
	err := Events(resp, func(_, data string) error {
		var e anthEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return err
		}
		switch e.Type {
		case "error":
			return fmt.Errorf("anthropic: %s", data)
		case "message_start":
			e.Message.Usage.apply(&usage)
		case "message_delta":
			if e.Usage != nil {
				e.Usage.apply(&usage)
			}
			if e.Delta != nil {
				stop = e.Delta.StopReason
			}
		case "content_block_start":
			if e.ContentBlock == nil {
				return fmt.Errorf("anthropic: block start without a block: %.200s", data)
			}
			parts[e.Index] = e.ContentBlock
		case "content_block_delta":
			if parts[e.Index] == nil || e.Delta == nil {
				return fmt.Errorf("anthropic: delta without a started block: %.200s", data)
			}
			parts[e.Index].add(*e.Delta)
		case "content_block_stop":
			if parts[e.Index] == nil {
				return fmt.Errorf("anthropic: stop without a started block: %.200s", data)
			}
			r, ok, err := parts[e.Index].record()
			if err != nil {
				return err
			}
			if ok {
				emit(r)
			}
			delete(parts, e.Index)
		case "message_stop":
			done = true
		}
		return nil
	})
	if err == nil && !done {
		err = fmt.Errorf("anthropic: stream ended before message_stop")
	}
	return statefile.Header{Usage: &usage, Stop: stop}, err
}
