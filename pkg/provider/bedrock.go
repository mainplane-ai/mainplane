package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"time"

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

func Bedrock(region, accessKey, secretKey, sessionToken string) Provider {
	return Provider{
		Name:     "bedrock",
		URL:      "https://bedrock-runtime." + region + ".amazonaws.com/model",
		Key:      secretKey,
		Envelope: bedrock{},
		Headers: func(r *http.Request, key string) {
			sigv4(r, region, accessKey, key, sessionToken, time.Now().UTC())
		},
		Endpoint: func(url, model string) string { return url + "/" + awsEscape(model) + "/converse-stream" },
	}
}

type bedrock struct{}

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

func (bedrock) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	body := bedReq{
		Messages:                     []bedMsg{},
		InferenceConfig:              bedInference{MaxTokens: bedrockMaxTokens},
		AdditionalModelRequestFields: bedAdditional{bedThinking{Type: "enabled", BudgetTokens: bedrockThinkingBudget}},
	}
	var marks []int
	if len(system) > 0 {
		body.System = []bedBlock{{Text: systemText(system)}, {CachePoint: cachePoint}}
		marks = append(marks, system[len(system)-1].N)
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
			content, err := bedAssistant(t.Records)
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
			marks = append(marks, t.Records[len(t.Records)-1].N)
		}
		body.Messages = append(body.Messages, bedMsg{Role: "user", Content: blocks})
	}
	cache, err := cacheHeader(bedrockCacheTTL, marks...)
	if err != nil {
		return nil, nil, err
	}
	b, err := marshal(body)
	return b, cache, err
}

func bedUser(recs []statefile.Record) []bedBlock {
	var blocks []bedBlock
	for _, r := range recs {
		switch r.Kind {
		case statefile.Result:
			res := &bedToolResult{ToolUseID: r.For}
			switch {
			case strings.HasPrefix(r.Type, "image/"):
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
		case statefile.Message, statefile.System, statefile.Summary, statefile.Error:
			blocks = append(blocks, bedBlock{Text: string(r.Body)})
		case statefile.Start, statefile.Link, statefile.Config, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	return blocks
}

func bedAssistant(recs []statefile.Record) ([]bedBlock, error) {
	var content []bedBlock
	for _, r := range recs {
		switch r.Kind {
		case statefile.Text:
			content = append(content, bedBlock{Text: string(r.Body)})
		case statefile.Thinking:
			if r.Provider == "bedrock" {
				content = append(content, bedBlock{ReasoningContent: r.Body})
			}
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return nil, fmt.Errorf("call %s: %w", r.ID, err)
			}
			content = append(content, bedBlock{ToolUse: &bedToolUse{ToolUseID: r.ID, Name: c.Name, Input: c.Arguments}})
		case statefile.Start, statefile.Link, statefile.Config, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Summary, statefile.Error:
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
		input := p.input
		if input == "" {
			input = "{}"
		}
		r.ID, r.Kind, r.Type = p.id, statefile.Call, "application/json"
		r.Body, err = marshal(Call{Name: p.name, Arguments: json.RawMessage(input)})
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

// awsEscape percent-encodes everything but RFC 3986 unreserved characters, as
// SigV4 wants each path segment. url.PathEscape keeps ':' which model ids use.
func awsEscape(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", c)
	}
	return sb.String()
}

// sigv4 signs r for the bedrock service. Signed headers: content-type, host,
// x-amz-date, and x-amz-security-token when a session token is set.
func sigv4(r *http.Request, region, access, secret, token string, now time.Time) {
	body, _ := r.GetBody()         // Step builds the request over a bytes.Reader; GetBody cannot fail
	payload, _ := io.ReadAll(body) // and neither can reading it
	sum := sha256.Sum256(payload)
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", stamp)
	if token != "" {
		r.Header.Set("X-Amz-Security-Token", token)
	}
	names := []string{"content-type", "host", "x-amz-date"}
	values := []string{r.Header.Get("Content-Type"), r.URL.Host, stamp}
	if token != "" {
		names, values = append(names, "x-amz-security-token"), append(values, token)
	}
	var canonical strings.Builder
	for i, n := range names {
		canonical.WriteString(n + ":" + values[i] + "\n")
	}
	signed := strings.Join(names, ";")
	// SigV4 encodes the path twice for every service but S3; the escaped path
	// holds only unreserved characters, '/', and %XX, so this is the second pass.
	uri := strings.ReplaceAll(r.URL.EscapedPath(), "%", "%25")
	request := strings.Join([]string{
		r.Method, uri, r.URL.RawQuery, canonical.String(), signed, hex.EncodeToString(sum[:]),
	}, "\n")
	scope := date + "/" + region + "/bedrock/aws4_request"
	reqSum := sha256.Sum256([]byte(request))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", stamp, scope, hex.EncodeToString(reqSum[:])}, "\n")
	key := []byte("AWS4" + secret)
	for _, s := range []string{date, region, "bedrock", "aws4_request"} {
		key = hmacSHA256(key, s)
	}
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		access, scope, signed, hex.EncodeToString(hmacSHA256(key, toSign))))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
