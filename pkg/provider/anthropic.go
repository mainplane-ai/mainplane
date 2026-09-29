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

// max_tokens is required by the Messages API. 16384 fits every current Claude
// model. Thinking is adaptive: Opus 4.7 and later reject the enabled type with
// a budget, and Opus 4.8 does not think when the field is left out. The 4.5
// models, which predate adaptive, reject it.
const (
	anthMaxTokens = 16384
	anthCacheTTL  = 300 // ephemeral, refreshed on every hit
)

func Anthropic(key string) Provider {
	return Provider{
		URL:      "https://api.anthropic.com/v1/messages",
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

type anthReq struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Stream    bool         `json:"stream"`
	System    []anthBlock  `json:"system,omitempty"`
	Thinking  anthThinking `json:"thinking"`
	Tools     []anthTool   `json:"tools,omitempty"`
	Messages  []anthMsg    `json:"messages"`
}

type anthThinking struct {
	Type string `json:"type"`
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
	body := anthReq{
		Model:     req.Model,
		MaxTokens: anthMaxTokens,
		Stream:    true,
		Thinking:  anthThinking{Type: "adaptive"},
		Messages:  []anthMsg{},
	}
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
			content, err := e.assistant(t.Records)
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
	b, err := encode(body, req.Params, "model", "stream", "system", "tools", "messages")
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
			case len(r.Body) > 0:
				b.Content = []anthBlock{{Type: "text", Text: string(r.Body)}}
			}
			blocks = append(blocks, b)
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(images, r.Type) {
				blocks = append(blocks, anthBlock{Type: "image", Source: &anthSource{Type: "base64", MediaType: r.Type, Data: base64.StdEncoding.EncodeToString(r.Body)}})
			} else {
				blocks = append(blocks, anthBlock{Type: "text", Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	return blocks
}

func (e anthropic) assistant(recs []statefile.Record) ([]any, error) {
	var content []any
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			content = append(content, anthBlock{Type: "text", Text: string(r.Body)})
		case statefile.Thinking:
			if r.Provider == e.Name() {
				content = append(content, json.RawMessage(r.Body))
			}
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return nil, fmt.Errorf("call %s: %w", r.ID, err)
			}
			content = append(content, anthBlock{Type: "tool_use", ID: r.ID, Name: c.Name, Input: c.Arguments})
		case statefile.Start, statefile.Config, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Error:
		}
	}
	return content, nil
}

type anthEvent struct {
	Type         string     `json:"type"`
	Index        int        `json:"index"`
	Message      *anthStart `json:"message"`
	ContentBlock *anthPart  `json:"content_block"`
	Delta        *anthDelta `json:"delta"`
	Usage        *anthUsage `json:"usage"`
	Error        *anthError `json:"error"`
}

type anthStart struct {
	Usage anthUsage `json:"usage"`
}

type anthError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
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
	err := Events(resp, func(_, data string) error {
		var e anthEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return err
		}
		switch e.Type {
		case "error":
			return fmt.Errorf("anthropic: %s: %s", e.Error.Type, e.Error.Message)
		case "message_start":
			e.Message.Usage.apply(&usage)
		case "message_delta":
			if e.Usage != nil {
				e.Usage.apply(&usage)
			}
		case "content_block_start":
			parts[e.Index] = e.ContentBlock
		case "content_block_delta":
			parts[e.Index].add(*e.Delta)
		case "content_block_stop":
			r, ok, err := parts[e.Index].record()
			if err != nil {
				return err
			}
			if ok {
				emit(r)
			}
			delete(parts, e.Index)
		}
		return nil
	})
	return statefile.Header{Usage: &usage}, err
}
