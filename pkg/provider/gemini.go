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

// geminiThinkingBudget is the legacy control that every Gemini 2.5 and 3 model still
// accepts. 2048 is modest; the model may use less.
const (
	geminiThinkingBudget = 2048
	geminiCacheTTL       = 300 // forecast: implicit caching has no documented ttl
)

func Gemini(key string) Provider {
	return Provider{
		URL:      "https://generativelanguage.googleapis.com/v1beta/models",
		Key:      key,
		Envelope: gemini{},
		Headers:  func(r *http.Request, key string) { r.Header.Set("x-goog-api-key", key) },
		Endpoint: func(url, model string) string { return url + "/" + model + ":streamGenerateContent?alt=sse" },
	}
}

type gemini struct{}

func (gemini) Name() string { return "gemini" }

// geminiMedia is what inlineData takes, named as http.DetectContentType names it.
var geminiMedia = slices.Concat(images, []string{"application/pdf", "audio/mpeg", "audio/aiff", "video/mp4", "video/webm"})

func (gemini) Accepts(typ string) bool { return slices.Contains(geminiMedia, typ) }

func (gemini) Owns(field string) bool {
	return slices.Contains([]string{"systemInstruction", "contents", "tools"}, field)
}

type gemReq struct {
	SystemInstruction *gemContent  `json:"systemInstruction,omitempty"`
	Contents          []gemContent `json:"contents"`
	Tools             []gemTools   `json:"tools,omitempty"`
	GenerationConfig  gemGenConfig `json:"generationConfig"`
}

type gemGenConfig struct {
	ThinkingConfig gemThinking `json:"thinkingConfig"`
}

type gemThinking struct {
	IncludeThoughts bool `json:"includeThoughts"`
	ThinkingBudget  int  `json:"thinkingBudget"`
}

type gemTools struct {
	FunctionDeclarations []gemDecl `json:"functionDeclarations"`
}

type gemDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}

// gemPart is one Gemini part in both directions. A thinking record's body is
// one of these: a thought part verbatim, or a signature alone when Gemini put
// the signature on the text or functionCall part that follows. Compile
// reattaches such a signature to the next part.
type gemPart struct {
	Text             string           `json:"text,omitempty"`
	Thought          bool             `json:"thought,omitempty"`
	ThoughtSignature string           `json:"thoughtSignature,omitempty"`
	FunctionCall     *gemFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *gemFunctionResp `json:"functionResponse,omitempty"`
	InlineData       *gemInline       `json:"inlineData,omitempty"`
}

type gemFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gemFunctionResp struct {
	ID       string    `json:"id,omitempty"`
	Name     string    `json:"name"`
	Response gemOutput `json:"response"`
}

type gemOutput struct {
	Output string `json:"output"`
}

type gemInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (e gemini) Compile(req Request) ([]byte, json.RawMessage, error) {
	system, rest := Split(req.Context)
	body := gemReq{
		Contents:         []gemContent{},
		GenerationConfig: gemGenConfig{gemThinking{IncludeThoughts: true, ThinkingBudget: geminiThinkingBudget}},
	}
	if len(system) > 0 {
		body.SystemInstruction = &gemContent{Parts: []gemPart{{Text: systemText(system)}}}
	}
	if len(req.Tools) > 0 {
		decls := make([]gemDecl, len(req.Tools))
		for i, t := range req.Tools {
			schema, err := gemSchema(t.Schema)
			if err != nil {
				return nil, nil, fmt.Errorf("tool %s: %w", t.Name, err)
			}
			decls[i] = gemDecl{Name: t.Name, Description: t.Description, Parameters: schema}
		}
		body.Tools = []gemTools{{decls}}
	}
	names := map[string]string{} // call id -> tool name, for functionResponse
	for _, t := range Turns(rest) {
		if !t.Assistant {
			body.Contents = append(body.Contents, gemUser(t.Records, names)...)
			continue
		}
		parts, err := e.model(t.Records, names)
		if err != nil {
			return nil, nil, err
		}
		if len(parts) > 0 {
			body.Contents = append(body.Contents, gemContent{Role: "model", Parts: parts})
		}
	}
	cache, err := cacheHeader(geminiCacheTTL, req.Context[len(req.Context)-1].ID)
	if err != nil {
		return nil, nil, err
	}
	b, err := encode(body, req.Params)
	return b, cache, err
}

// gemSchema strips the JSON Schema keywords the parameters field rejects.
func gemSchema(schema json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(schema, &v); err != nil {
		return nil, err
	}
	var strip func(v any)
	strip = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			delete(v, "$schema")
			delete(v, "additionalProperties")
			for _, c := range v {
				strip(c)
			}
		case []any:
			for _, c := range v {
				strip(c)
			}
		}
	}
	strip(v)
	return marshal(v)
}

// gemUser returns one content of function responses and one of everything
// else. Gemini wants function responses directly after the function calls.
func gemUser(recs []statefile.Record, names map[string]string) []gemContent {
	var responses, parts []gemPart
	for _, r := range recs {
		switch r.Kind {
		case statefile.Result:
			fr := &gemFunctionResp{ID: r.For, Name: names[r.For], Response: gemOutput{Output: string(r.Body)}}
			if slices.Contains(geminiMedia, r.Type) {
				fr.Response.Output = "media attached below"
				parts = append(parts, gemPart{InlineData: &gemInline{MimeType: r.Type, Data: base64.StdEncoding.EncodeToString(r.Body)}})
			}
			responses = append(responses, gemPart{FunctionResponse: fr})
		case statefile.Message, statefile.System, statefile.Error:
			if slices.Contains(geminiMedia, r.Type) {
				parts = append(parts, gemPart{InlineData: &gemInline{MimeType: r.Type, Data: base64.StdEncoding.EncodeToString(r.Body)}})
			} else {
				parts = append(parts, gemPart{Text: string(r.Body)})
			}
		case statefile.Start, statefile.Config, statefile.Text, statefile.Thinking, statefile.Call, statefile.Step:
		}
	}
	var out []gemContent
	if len(responses) > 0 {
		out = append(out, gemContent{Role: "user", Parts: responses})
	}
	if len(parts) > 0 {
		out = append(out, gemContent{Role: "user", Parts: parts})
	}
	return out
}

func (e gemini) model(recs []statefile.Record, names map[string]string) ([]gemPart, error) {
	var parts []gemPart
	var pending string // signature waiting for the part it was emitted on
	for _, r := range recs {
		switch r.Kind {
		case statefile.Thinking:
			if r.Provider != e.Name() {
				continue
			}
			var p gemPart
			if err := json.Unmarshal(r.Body, &p); err != nil {
				return nil, fmt.Errorf("thinking %s: %w", r.ID, err)
			}
			if !p.Thought {
				pending = p.ThoughtSignature
				continue
			}
			parts = append(parts, p)
		case statefile.Text:
			parts = append(parts, gemPart{Text: string(r.Body), ThoughtSignature: pending})
			pending = ""
		case statefile.Call:
			var c Call
			if err := json.Unmarshal(r.Body, &c); err != nil {
				return nil, fmt.Errorf("call %s: %w", r.ID, err)
			}
			names[r.ID] = c.Name
			parts = append(parts, gemPart{
				FunctionCall:     &gemFunctionCall{ID: r.ID, Name: c.Name, Args: c.Arguments},
				ThoughtSignature: pending,
			})
			pending = ""
		case statefile.Start, statefile.Config, statefile.System, statefile.Message,
			statefile.Step, statefile.Result, statefile.Error:
		}
	}
	return parts, nil
}

type gemChunk struct {
	Candidates []struct {
		Content gemContent `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// gemBlock accumulates consecutive text parts of one kind. Gemini streams a
// part per chunk with no block boundaries, so a kind change closes the block.
type gemBlock struct {
	thought bool
	text    string
	sig     string
}

func (b *gemBlock) flush(emit func(statefile.Record)) error {
	defer func() { *b = gemBlock{} }()
	if b.text == "" && b.sig == "" {
		return nil
	}
	if b.thought {
		body, err := marshal(gemPart{Thought: true, Text: b.text, ThoughtSignature: b.sig})
		if err != nil {
			return err
		}
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Thinking, Type: "application/json"}, Body: body})
		return nil
	}
	if err := gemSignature(b.sig, emit); err != nil {
		return err
	}
	if b.text != "" {
		emit(statefile.Record{Header: statefile.Header{Kind: statefile.Text, Type: "text/plain"}, Body: []byte(b.text)})
	}
	return nil
}

// gemSignature emits a signature-only thinking record ahead of the part that carried it.
func gemSignature(sig string, emit func(statefile.Record)) error {
	if sig == "" {
		return nil
	}
	body, err := marshal(gemPart{ThoughtSignature: sig})
	if err != nil {
		return err
	}
	emit(statefile.Record{Header: statefile.Header{Kind: statefile.Thinking, Type: "application/json"}, Body: body})
	return nil
}

// add folds one streamed part into the block, closing it on a kind change.
// A function call closes the block and is emitted at once.
func (b *gemBlock) add(p gemPart, emit func(statefile.Record)) error {
	if p.InlineData != nil {
		return nil
	}
	if p.FunctionCall != nil {
		if err := b.flush(emit); err != nil {
			return err
		}
		return gemCall(p, emit)
	}
	if b.thought != p.Thought {
		if err := b.flush(emit); err != nil {
			return err
		}
		b.thought = p.Thought
	}
	b.text += p.Text
	if p.ThoughtSignature != "" {
		b.sig = p.ThoughtSignature
	}
	return nil
}

func gemCall(p gemPart, emit func(statefile.Record)) error {
	if err := gemSignature(p.ThoughtSignature, emit); err != nil {
		return err
	}
	// Gemini 3 returns an id; older models match responses by name and order.
	id := p.FunctionCall.ID
	if id == "" {
		id = statefile.NewID()
	}
	r, err := callRecord(id, p.FunctionCall.Name, string(p.FunctionCall.Args))
	if err != nil {
		return err
	}
	emit(r)
	return nil
}

func (gemini) Stream(resp io.Reader, emit func(statefile.Record)) (statefile.Header, error) {
	var usage statefile.Usage
	var block gemBlock
	err := Events(resp, func(_, data string) error {
		var c gemChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return err
		}
		if c.Error != nil {
			return fmt.Errorf("gemini: %d: %s", c.Error.Code, c.Error.Message)
		}
		if u := c.UsageMetadata; u != nil {
			usage = statefile.Usage{
				Input:     u.PromptTokenCount - u.CachedContentTokenCount,
				Output:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
				CacheRead: u.CachedContentTokenCount,
			}
		}
		for _, cand := range c.Candidates {
			for _, p := range cand.Content.Parts {
				if err := block.add(p, emit); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return statefile.Header{}, err
	}
	if err := block.flush(emit); err != nil {
		return statefile.Header{}, err
	}
	return statefile.Header{Usage: &usage}, nil
}
