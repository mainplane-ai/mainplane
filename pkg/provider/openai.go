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

const (
	respEffort   = "low"
	respCacheTTL = 300 // forecast: openai evicts after 5 to 10 idle minutes
)

func OpenAI(key string) Provider {
	return Provider{
		URL:      "https://api.openai.com/v1/responses",
		Key:      key,
		Envelope: openai{},
		Headers:  bearer,
	}
}

func bearer(r *http.Request, key string) { r.Header.Set("Authorization", "Bearer "+key) }

type openai struct{}

func (openai) Name() string { return "openai" }

func (openai) Accepts(typ string) bool { return slices.Contains(images, typ) }

type respReq struct {
	Model        string        `json:"model"`
	Store        bool          `json:"store"`
	CacheKey     string        `json:"prompt_cache_key,omitempty"`
	Stream       bool          `json:"stream"`
	Include      []string      `json:"include"`
	Reasoning    respReasoning `json:"reasoning"`
	Instructions string        `json:"instructions,omitempty"`
	Tools        []respTool    `json:"tools,omitempty"`
	Input        []any         `json:"input"`
}

type respReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
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

func (e openai) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	body := respReq{
		Model:        req.Model,
		CacheKey:     req.Key,
		Stream:       true,
		Include:      []string{"reasoning.encrypted_content"},
		Reasoning:    respReasoning{Effort: respEffort, Summary: "auto"},
		Instructions: systemText(system),
		Input:        []any{},
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
			return nil, nil, err
		}
		body.Input = append(body.Input, items...)
	}
	cache, err := cacheHeader(respCacheTTL, req.Context[len(req.Context)-1].ID)
	if err != nil {
		return nil, nil, err
	}
	b, err := marshal(body)
	return b, cache, err
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
			out := respOutput{Type: "function_call_output", CallID: r.For, Output: string(r.Body)}
			if slices.Contains(images, r.Type) {
				out.Output = []respPart{{Type: "input_image", ImageURL: imageURL(r), Detail: "auto"}}
			}
			items = append(items, out)
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(images, r.Type) {
				parts = append(parts, respPart{Type: "input_image", ImageURL: imageURL(r), Detail: "auto"})
			} else {
				parts = append(parts, respPart{Type: "input_text", Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
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
		case statefile.Start, statefile.Config, statefile.System, statefile.Message,
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
	Usage *respUsage `json:"usage"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
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

func (openai) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	var usage statefile.Usage
	err := Events(resp, func(_, data string) error {
		var e respEvent
		if err := json.Unmarshal([]byte(data), &e); err != nil {
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
			u := e.Response.Usage
			usage = statefile.Usage{
				Input:     u.InputTokens - u.InputTokensDetails.CachedTokens,
				Output:    u.OutputTokens,
				CacheRead: u.InputTokensDetails.CachedTokens,
			}
		}
		return nil
	})
	return statefile.Header{Usage: &usage}, err
}
