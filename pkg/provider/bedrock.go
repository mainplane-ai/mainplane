package provider

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// maxTokens is required by Converse for Claude. 16384 fits every current
// Claude model and leaves room above the thinking budget. The thinking field
// is Claude's; other Bedrock models are not covered.
const (
	bedrockMaxTokens      = 16384
	bedrockThinkingBudget = 1024
	bedrockCacheTTL       = 300
)

// Bedrock authenticates with a Bedrock API key (AWS_BEARER_TOKEN_BEDROCK),
// not SigV4. Model is an inference profile id such as
// us.anthropic.claude-haiku-4-5-20251001-v1:0.
func Bedrock(region, key string) Provider {
	return Provider{
		URL:      "https://bedrock-runtime." + region + ".amazonaws.com/model",
		Key:      key,
		Envelope: bedrock{},
		Headers:  func(r *http.Request, key string) { r.Header.Set("Authorization", "Bearer "+key) },
		Endpoint: func(url, model string) string { return url + "/" + model + "/converse-stream" },
	}
}

type bedrock struct{}

func (bedrock) Name() string { return "bedrock" }

func (bedrock) Accepts(typ string) bool { return slices.Contains(images, typ) }

type bedReq struct {
	System                       []bedBlock     `json:"system,omitempty"`
	Messages                     []bedMsg       `json:"messages"`
	InferenceConfig              bedInference   `json:"inferenceConfig"`
	ToolConfig                   *bedToolConfig `json:"toolConfig,omitempty"`
	AdditionalModelRequestFields bedAdditional  `json:"additionalModelRequestFields"`
}

type bedInference struct {
	MaxTokens int `json:"maxTokens"`
}

type bedAdditional struct {
	Thinking bedThinking `json:"thinking"`
}

type bedThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type bedToolConfig struct {
	Tools []bedTool `json:"tools"`
}

type bedTool struct {
	ToolSpec bedToolSpec `json:"toolSpec"`
}

type bedToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema bedInputSchema `json:"inputSchema"`
}

type bedInputSchema struct {
	JSON json.RawMessage `json:"json"`
}

type bedMsg struct {
	Role    string     `json:"role"`
	Content []bedBlock `json:"content"`
}

// bedBlock is one Converse content block; exactly one field is set.
// ReasoningContent is a thinking record's body replayed as is.
type bedBlock struct {
	Text             string          `json:"text,omitempty"`
	Image            *bedImage       `json:"image,omitempty"`
	ToolUse          *bedToolUse     `json:"toolUse,omitempty"`
	ToolResult       *bedToolResult  `json:"toolResult,omitempty"`
	ReasoningContent json.RawMessage `json:"reasoningContent,omitempty"`
	CachePoint       *bedCachePoint  `json:"cachePoint,omitempty"`
}

type bedImage struct {
	Format string    `json:"format"`
	Source bedSource `json:"source"`
}

type bedSource struct {
	Bytes string `json:"bytes"` // base64
}

type bedToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type bedToolResult struct {
	ToolUseID string     `json:"toolUseId"`
	Content   []bedBlock `json:"content"`
}

type bedCachePoint struct {
	Type string `json:"type"`
}

// bedReasoning is the shape of a Bedrock thinking record body.
type bedReasoning struct {
	ReasoningText   *bedReasoningText `json:"reasoningText,omitempty"`
	RedactedContent string            `json:"redactedContent,omitempty"`
}

type bedReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

var cachePoint = &bedCachePoint{Type: "default"}

func (e bedrock) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	body := bedReq{
		Messages:                     []bedMsg{},
		InferenceConfig:              bedInference{MaxTokens: bedrockMaxTokens},
		AdditionalModelRequestFields: bedAdditional{bedThinking{Type: "enabled", BudgetTokens: bedrockThinkingBudget}},
	}
	var marks []string
	if len(system) > 0 {
		body.System = []bedBlock{{Text: systemText(system)}, {CachePoint: cachePoint}}
		marks = append(marks, system[len(system)-1].ID)
	}
	if len(req.Tools) > 0 {
		body.ToolConfig = &bedToolConfig{}
		for _, t := range req.Tools {
			body.ToolConfig.Tools = append(body.ToolConfig.Tools, bedTool{bedToolSpec{
				Name: t.Name, Description: t.Description, InputSchema: bedInputSchema{JSON: t.Schema},
			}})
		}
	}
	turns := Turns(rest)
	for i, t := range turns {
		if t.Assistant {
			content, err := e.assistant(t.Records)
			if err != nil {
				return nil, nil, err
			}
			if len(content) > 0 {
				body.Messages = append(body.Messages, bedMsg{Role: "assistant", Content: content})
			}
			continue
		}
		blocks := bedUser(t.Records)
		if i == len(turns)-1 {
			blocks = append(blocks, bedBlock{CachePoint: cachePoint})
			marks = append(marks, t.Records[len(t.Records)-1].ID)
		}
		body.Messages = append(body.Messages, bedMsg{Role: "user", Content: blocks})
	}
	cache, err := cacheHeader(bedrockCacheTTL, marks...)
	if err != nil {
		return nil, nil, err
	}
	b, err := encode(body, req.Params, "system", "messages", "toolConfig")
	return b, cache, err
}

func bedUser(recs []statefile.Record) []bedBlock {
	var blocks []bedBlock
	for _, r := range recs {
		switch r.Kind {
		case statefile.Result:
			res := &bedToolResult{ToolUseID: r.For}
			switch {
			case slices.Contains(images, r.Type):
				res.Content = []bedBlock{{Image: &bedImage{
					Format: strings.TrimPrefix(r.Type, "image/"),
					Source: bedSource{Bytes: base64.StdEncoding.EncodeToString(r.Body)},
				}}}
			case len(r.Body) > 0:
				res.Content = []bedBlock{{Text: string(r.Body)}}
			default:
				res.Content = []bedBlock{{Text: "(empty)"}} // Converse rejects empty text blocks
			}
			blocks = append(blocks, bedBlock{ToolResult: res})
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(images, r.Type) {
				blocks = append(blocks, bedBlock{Image: &bedImage{Format: strings.TrimPrefix(r.Type, "image/"), Source: bedSource{Bytes: base64.StdEncoding.EncodeToString(r.Body)}}})
			} else {
				blocks = append(blocks, bedBlock{Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	return blocks
}

func (e bedrock) assistant(recs []statefile.Record) ([]bedBlock, error) {
	var content []bedBlock
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			content = append(content, bedBlock{Text: string(r.Body)})
		case statefile.Thinking:
			if r.Provider == e.Name() {
				content = append(content, bedBlock{ReasoningContent: r.Body})
			}
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return nil, fmt.Errorf("call %s: %w", r.ID, err)
			}
			content = append(content, bedBlock{ToolUse: &bedToolUse{ToolUseID: r.ID, Name: c.Name, Input: c.Arguments}})
		case statefile.Start, statefile.Config, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Error:
		}
	}
	return content, nil
}

// bedEvent is the payload of any converse-stream event; the event name is in
// the frame's :event-type header.
type bedEvent struct {
	ContentBlockIndex int `json:"contentBlockIndex"`
	Start             struct {
		ToolUse *struct {
			ToolUseID string `json:"toolUseId"`
			Name      string `json:"name"`
		} `json:"toolUse"`
	} `json:"start"`
	Delta bedDelta `json:"delta"`
	Usage *struct {
		InputTokens           int `json:"inputTokens"`
		OutputTokens          int `json:"outputTokens"`
		CacheReadInputTokens  int `json:"cacheReadInputTokens"`
		CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	} `json:"usage"`
	Message string `json:"message"` // exceptions
}

type bedDelta struct {
	Text    *string `json:"text"`
	ToolUse *struct {
		Input string `json:"input"`
	} `json:"toolUse"`
	ReasoningContent *struct {
		Text            string `json:"text"`
		Signature       string `json:"signature"`
		RedactedContent string `json:"redactedContent"`
	} `json:"reasoningContent"`
}

// bedPart is a content block under assembly, keyed by contentBlockIndex.
// Text and reasoning blocks get no contentBlockStart, so the first delta
// decides their kind.
type bedPart struct {
	kind      string // text, toolUse, reasoning
	id, name  string
	text      string
	signature string
	redacted  string
	input     string
}

func (p *bedPart) add(d bedDelta) {
	switch {
	case d.Text != nil:
		p.kind = "text"
		p.text += *d.Text
	case d.ToolUse != nil:
		p.input += d.ToolUse.Input
	case d.ReasoningContent != nil:
		p.kind = "reasoning"
		p.text += d.ReasoningContent.Text
		p.signature += d.ReasoningContent.Signature
		p.redacted += d.ReasoningContent.RedactedContent
	}
}

// record turns a finished block into its record. Blocks of no known kind yield none.
func (p *bedPart) record() (statefile.Record, bool, error) {
	var r statefile.Record
	var err error
	switch p.kind {
	case "":
		return r, false, nil
	case "text":
		r.Kind, r.Type, r.Body = statefile.Text, "text/plain", []byte(p.text)
	case "toolUse":
		r, err = callRecord(p.id, p.name, p.input)
	case "reasoning":
		r.Kind, r.Type = statefile.Thinking, "application/json"
		reasoning := bedReasoning{RedactedContent: p.redacted}
		if p.redacted == "" {
			reasoning.ReasoningText = &bedReasoningText{Text: p.text, Signature: p.signature}
		}
		r.Body, err = marshal(reasoning)
	}
	return r, err == nil, err
}

func (bedrock) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	parts := map[int]*bedPart{}
	var usage statefile.Usage
	for {
		headers, payload, err := readFrame(resp)
		if errors.Is(err, io.EOF) {
			return statefile.Header{Usage: &usage}, nil
		}
		if err != nil {
			return statefile.Header{}, err
		}
		var e bedEvent
		if err := json.Unmarshal(payload, &e); err != nil {
			return statefile.Header{}, err
		}
		if headers[":message-type"] != "event" {
			return statefile.Header{}, fmt.Errorf("bedrock: %s: %s", headers[":exception-type"], e.Message)
		}
		switch headers[":event-type"] {
		case "contentBlockStart":
			if e.Start.ToolUse != nil {
				parts[e.ContentBlockIndex] = &bedPart{kind: "toolUse", id: e.Start.ToolUse.ToolUseID, name: e.Start.ToolUse.Name}
			}
		case "contentBlockDelta":
			if parts[e.ContentBlockIndex] == nil {
				parts[e.ContentBlockIndex] = &bedPart{}
			}
			parts[e.ContentBlockIndex].add(e.Delta)
		case "contentBlockStop":
			p := parts[e.ContentBlockIndex]
			if p == nil {
				continue
			}
			r, ok, err := p.record()
			if err != nil {
				return statefile.Header{}, err
			}
			if ok {
				emit(r)
			}
			delete(parts, e.ContentBlockIndex)
		case "metadata":
			if e.Usage != nil {
				usage = statefile.Usage{
					Input: e.Usage.InputTokens, Output: e.Usage.OutputTokens,
					CacheRead: e.Usage.CacheReadInputTokens, CacheWrite: e.Usage.CacheWriteInputTokens,
				}
			}
		}
	}
}

// readFrame decodes one AWS event-stream message: a 12 byte prelude (total
// length, headers length, prelude CRC), typed headers, payload, message CRC.
// io.EOF at a frame boundary is returned as is.
func readFrame(r io.Reader) (map[string]string, []byte, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, nil, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	hlen := binary.BigEndian.Uint32(prelude[4:8])
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return nil, nil, errors.New("event stream: prelude crc")
	}
	if total < 16 || hlen > total-16 {
		return nil, nil, fmt.Errorf("event stream: bad lengths %d %d", total, hlen)
	}
	rest := make([]byte, total-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, nil, fmt.Errorf("event stream: %w", err)
	}
	sum := crc32.NewIEEE()
	sum.Write(prelude[:])
	sum.Write(rest[:len(rest)-4])
	if sum.Sum32() != binary.BigEndian.Uint32(rest[len(rest)-4:]) {
		return nil, nil, errors.New("event stream: message crc")
	}
	headers, err := eventHeaders(rest[:hlen])
	if err != nil {
		return nil, nil, err
	}
	return headers, rest[hlen : len(rest)-4], nil
}

// eventHeaders parses event-stream headers. Only string values are kept; the
// other types are skipped by their fixed or prefixed length.
func eventHeaders(b []byte) (map[string]string, error) {
	fixed := map[byte]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16}
	out := map[string]string{}
	for len(b) > 0 {
		n := int(b[0])
		if len(b) < 2+n {
			return nil, errors.New("event stream: short header")
		}
		name, typ := string(b[1:1+n]), b[1+n]
		b = b[2+n:]
		size, ok := fixed[typ]
		if !ok { // 6 bytes, 7 string: uint16 length prefix
			if len(b) < 2 {
				return nil, errors.New("event stream: short header")
			}
			size = int(binary.BigEndian.Uint16(b)) + 2
		}
		if len(b) < size {
			return nil, errors.New("event stream: short header")
		}
		if typ == 7 {
			out[name] = string(b[2:size])
		}
		b = b[size:]
	}
	return out, nil
}
