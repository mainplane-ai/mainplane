package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// OpenAIChat is the Chat Completions envelope for OpenAI-compatible endpoints.
// Thinking records are never replayed.
func OpenAIChat(base, key string) Provider {
	return Provider{
		Name:     "openai-chat",
		URL:      strings.TrimSuffix(base, "/") + "/v1/chat/completions",
		Key:      key,
		Envelope: openaiChat{},
		Headers:  bearer,
	}
}

type openaiChat struct{}

type chatReq struct {
	Model         string            `json:"model"`
	Stream        bool              `json:"stream"`
	StreamOptions chatStreamOptions `json:"stream_options"`
	Messages      []chatMsg         `json:"messages"`
	Tools         []chatTool        `json:"tools,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatMsg content is a string for system, assistant, and tool messages and
// []chatPart for user messages.
type chatMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatCallBody `json:"function"`
}

type chatCallBody struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (openaiChat) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	body := chatReq{Model: req.Model, Stream: true, StreamOptions: chatStreamOptions{IncludeUsage: true}, Messages: []chatMsg{}}
	if len(system) > 0 {
		body.Messages = append(body.Messages, chatMsg{Role: "system", Content: systemText(system)})
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, chatTool{Type: "function", Function: chatFunction{t.Name, t.Description, t.Schema}})
	}
	for _, t := range Turns(rest) {
		if !t.Assistant {
			body.Messages = append(body.Messages, chatUser(t.Records)...)
			continue
		}
		m, err := chatAssistant(t.Records)
		if err != nil {
			return nil, nil, err
		}
		body.Messages = append(body.Messages, m)
	}
	b, err := marshal(body) // no cache header: third-party caches are their own business
	return b, nil, err
}

// chatUser emits one tool message per result, then one user message holding
// image results and every text record of the turn.
func chatUser(recs []statefile.Record) []chatMsg {
	var msgs []chatMsg
	var parts []chatPart
	for _, r := range recs {
		switch r.Kind {
		case statefile.Result:
			m := chatMsg{Role: "tool", ToolCallID: r.For, Content: string(r.Body)}
			if strings.HasPrefix(r.Type, "image/") {
				m.Content = "(see attached image)"
				parts = append(parts, chatPart{Type: "image_url", ImageURL: &chatImageURL{URL: imageURL(r)}})
			}
			msgs = append(msgs, m)
		case statefile.Message, statefile.System, statefile.Summary, statefile.Error:
			parts = append(parts, chatPart{Type: "text", Text: string(r.Body)})
		case statefile.Start, statefile.Link, statefile.Config, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	if len(parts) > 0 {
		msgs = append(msgs, chatMsg{Role: "user", Content: parts})
	}
	return msgs
}

func chatAssistant(recs []statefile.Record) (chatMsg, error) {
	m := chatMsg{Role: "assistant"}
	var text []string
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			text = append(text, string(r.Body))
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return m, fmt.Errorf("call %s: %w", r.ID, err)
			}
			m.ToolCalls = append(m.ToolCalls, chatToolCall{ID: r.ID, Type: "function", Function: chatCallBody{c.Name, string(c.Arguments)}})
		case statefile.Start, statefile.Link, statefile.Config, statefile.System, statefile.Message, statefile.Thinking,
			statefile.Step, statefile.Result, statefile.Summary, statefile.Error:
		}
	}
	if len(text) > 0 {
		m.Content = strings.Join(text, "")
	}
	return m, nil
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int          `json:"index"`
				ID       string       `json:"id"`
				Function chatCallBody `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream accumulates text and tool call deltas by index and emits them when
// the stream ends, text first: Chat Completions marks no block boundaries.
func (openaiChat) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	var text strings.Builder
	var calls []*chatToolCall
	var usage statefile.Usage
	err := Events(resp, func(_, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var c chatChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return err
		}
		if c.Error != nil {
			return fmt.Errorf("openai-chat: %s", c.Error.Message)
		}
		if c.Usage != nil {
			usage = statefile.Usage{
				Input:     c.Usage.PromptTokens - c.Usage.PromptTokensDetails.CachedTokens,
				Output:    c.Usage.CompletionTokens,
				CacheRead: c.Usage.PromptTokensDetails.CachedTokens,
			}
		}
		if len(c.Choices) == 0 {
			return nil
		}
		d := c.Choices[0].Delta
		text.WriteString(d.Content)
		for _, tc := range d.ToolCalls {
			for len(calls) <= tc.Index {
				calls = append(calls, &chatToolCall{})
			}
			call := calls[tc.Index]
			call.ID += tc.ID
			call.Function.Name += tc.Function.Name
			call.Function.Arguments += tc.Function.Arguments
		}
		return nil
	})
	if err != nil {
		return statefile.Header{}, err
	}
	if text.Len() > 0 {
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte(text.String())})
	}
	for _, c := range calls {
		args := c.Function.Arguments
		if args == "" {
			args = "{}"
		}
		body, err := marshal(Call{Name: c.Function.Name, Arguments: json.RawMessage(args)})
		if err != nil {
			return statefile.Header{}, err
		}
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Call, ID: c.ID, Type: "application/json"}, Body: body})
	}
	return statefile.Header{Usage: &usage}, nil
}
