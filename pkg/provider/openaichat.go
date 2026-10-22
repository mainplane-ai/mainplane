package provider

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// OpenAIChat is the Chat Completions envelope for OpenAI-compatible endpoints.
// Thinking is reasoning_content, the field DeepSeek, Kimi, GLM and MiMo stream
// and want back in a tool loop. OpenRouter and OpenCode Go stream it as
// reasoning instead and read either field back, so it replays as
// reasoning_content everywhere; a server without it sends none. OpenRouter
// also streams reasoning_details, which hold the upstream's signed or
// encrypted reasoning (Claude, GPT, Gemini). Where a step has them they are
// its thinking record, a JSON array, replayed as reasoning_details.
func OpenAIChat(url, key string) Provider {
	return Provider{
		URL:      base(url, "https://api.openai.com/v1") + "/chat/completions",
		Key:      key,
		Envelope: openaiChat{},
		Headers:  bearer,
	}
}

type openaiChat struct{}

func (openaiChat) Name() string { return "openai-chat" }

func (openaiChat) Accepts(typ string) bool { return slices.Contains(images, typ) }

func (openaiChat) Owns(field string) bool {
	return slices.Contains([]string{"model", "stream", "stream_options", "prompt_cache_key", "messages", "tools"}, field)
}

type chatReq struct {
	Model         string            `json:"model"`
	Stream        bool              `json:"stream"`
	StreamOptions chatStreamOptions `json:"stream_options"`
	CacheKey      string            `json:"prompt_cache_key,omitempty"`
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
	Role       string            `json:"role"`
	Content    any               `json:"content,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall    `json:"tool_calls,omitempty"`
	Reasoning  string            `json:"reasoning_content,omitempty"`
	Details    []json.RawMessage `json:"reasoning_details,omitempty"`
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

func (e openaiChat) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	body := chatReq{Model: req.Model, Stream: true, StreamOptions: chatStreamOptions{IncludeUsage: true}, CacheKey: req.Key, Messages: []chatMsg{}}
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
		m, err := e.assistant(t.Records)
		if err != nil {
			return nil, nil, err
		}
		body.Messages = append(body.Messages, m)
	}
	b, err := encode(body, req.Params) // no cache header: the server caches the prefix itself, prompt_cache_key routes to it
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
			m := chatMsg{Role: "tool", ToolCallID: r.For, Content: noted(string(r.Body), r.Note)}
			if slices.Contains(images, r.Type) {
				m.Content = noted("(see attached image)", r.Note)
				parts = append(parts, chatPart{Type: "image_url", ImageURL: &chatImageURL{URL: imageURL(r)}})
			}
			msgs = append(msgs, m)
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(images, r.Type) {
				parts = append(parts, chatPart{Type: "image_url", ImageURL: &chatImageURL{URL: imageURL(r)}})
			} else {
				parts = append(parts, chatPart{Type: "text", Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Name, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	if len(parts) > 0 {
		msgs = append(msgs, chatMsg{Role: "user", Content: parts})
	}
	return msgs
}

func (e openaiChat) assistant(recs []statefile.Record) (chatMsg, error) {
	m := chatMsg{Role: "assistant"}
	var text, reasoning []string
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			text = append(text, string(r.Body))
		case statefile.Thinking:
			if r.Provider != e.Name() {
				continue
			}
			if r.Type != "application/json" {
				reasoning = append(reasoning, string(r.Body))
				continue
			}
			var d []json.RawMessage
			if err := json.Unmarshal(r.Body, &d); err != nil {
				return m, fmt.Errorf("thinking %s: %w", r.ID, err)
			}
			m.Details = append(m.Details, d...)
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return m, fmt.Errorf("call %s: %w", r.ID, err)
			}
			m.ToolCalls = append(m.ToolCalls, chatToolCall{ID: r.ID, Type: "function", Function: chatCallBody{c.Name, string(c.Arguments)}})
		case statefile.Start, statefile.Config, statefile.Name, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Error:
		}
	}
	if len(text) > 0 {
		m.Content = strings.Join(text, "")
	}
	m.Reasoning = strings.Join(reasoning, "")
	return m, nil
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string   `json:"content"`
			Reasoning string   `json:"reasoning_content"`
			Alias     string   `json:"reasoning"`         // OpenRouter, OpenCode Go
			Details   []detail `json:"reasoning_details"` // OpenRouter
			ToolCalls []struct {
				Index    int          `json:"index"`
				ID       string       `json:"id"`
				Function chatCallBody `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		CacheHitTokens      int `json:"prompt_cache_hit_tokens"` // DeepSeek's name for cached_tokens
		PromptTokensDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"` // Kimi
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream accumulates reasoning, text and tool call deltas by index and emits
// them when the stream ends, in that order: Chat Completions marks no block
// boundaries.
func (openaiChat) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	var reasoning, text strings.Builder
	var details []detail
	var calls []*chatToolCall
	var usage statefile.Usage
	var stop string
	var done bool
	err := Events(resp, func(_, data string) error {
		if data == "[DONE]" {
			done = true
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
			u := c.Usage
			read := max(u.PromptTokensDetails.CachedTokens, u.CacheHitTokens)
			usage = statefile.Usage{
				Input:      u.PromptTokens - read - u.PromptTokensDetails.CacheWriteTokens,
				Output:     u.CompletionTokens,
				CacheRead:  read,
				CacheWrite: u.PromptTokensDetails.CacheWriteTokens,
			}
		}
		if len(c.Choices) == 0 {
			return nil
		}
		if f := c.Choices[0].FinishReason; f != "" {
			stop = f
		}
		d := c.Choices[0].Delta
		reasoning.WriteString(cmp.Or(d.Reasoning, d.Alias)) // a server that sends both sends the same text
		var err error
		if details, err = addDetails(details, d.Details); err != nil {
			return err
		}
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
	if err == nil && !done {
		err = fmt.Errorf("openai-chat: stream ended before [DONE]")
	}
	if err != nil {
		return statefile.Header{}, err
	}
	if r, ok, err := chatThinking(details, reasoning.String()); err != nil {
		return statefile.Header{}, err
	} else if ok {
		emit(r)
	}
	if text.Len() > 0 {
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte(text.String())})
	}
	for _, c := range calls {
		r, err := callRecord(c.ID, c.Function.Name, c.Function.Arguments)
		if err != nil {
			return statefile.Header{}, err
		}
		emit(r)
	}
	return statefile.Header{Usage: &usage, Stop: stop}, nil
}

// chatThinking is a step's thinking record: its reasoning_details if it has
// them, else its reasoning text.
func chatThinking(details []detail, reasoning string) (statefile.Record, bool, error) {
	r := statefile.Record{Header: statefile.Header{Kind: statefile.Thinking, Type: "text/plain"}, Body: []byte(reasoning)}
	if len(details) == 0 {
		return r, reasoning != "", nil
	}
	body, err := marshal(details)
	r.Type, r.Body = "application/json", body
	return r, err == nil, err
}

// detail is one reasoning_details entry, kept field by field as streamed.
type detail map[string]json.RawMessage

// detailText are the reasoning_details fields a stream sends in pieces.
var detailText = []string{"text", "summary", "data", "signature"}

// addDetails merges streamed reasoning_details entries into the list.
// OpenRouter says only to concatenate the chunks in order. An entry with the
// type and index of the last one continues it, as OpenRouter's SDK merges
// consecutive text and summary entries: its text fields append, its other
// fields fill those the last one lacks. Gemini sends a text entry and then an
// encrypted one under the same index, so type and index both decide.
func addDetails(all, ds []detail) ([]detail, error) {
	for _, d := range ds {
		n := len(all)
		if n == 0 || !bytes.Equal(all[n-1]["type"], d["type"]) || !bytes.Equal(all[n-1]["index"], d["index"]) {
			all = append(all, d)
			continue
		}
		last := all[n-1]
		for k, v := range d {
			old, ok := last[k]
			if !ok || string(old) == "null" {
				last[k] = v
				continue
			}
			if !slices.Contains(detailText, k) || string(v) == "null" {
				continue
			}
			var a, b string
			if err := json.Unmarshal(old, &a); err != nil {
				return nil, fmt.Errorf("openai-chat: reasoning_details %s: %w", k, err)
			}
			if err := json.Unmarshal(v, &b); err != nil {
				return nil, fmt.Errorf("openai-chat: reasoning_details %s: %w", k, err)
			}
			s, err := marshal(a + b)
			if err != nil {
				return nil, err
			}
			last[k] = s
		}
	}
	return all, nil
}
