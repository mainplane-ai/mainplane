// Package statefile reads and writes the append-only files that hold an agent
// session. A record is one JSON header line, a body of exactly len bytes, and a
// newline. The body is never escaped.
package statefile

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type Kind string

const (
	Start    Kind = "start"
	Config   Kind = "config"
	System   Kind = "system"
	Message  Kind = "message"
	Text     Kind = "text"
	Thinking Kind = "thinking"
	Call     Kind = "call"
	Step     Kind = "step"
	Result   Kind = "result"
	Error    Kind = "error"
)

// Usage counts tokens. Input excludes cache reads and writes on every
// provider, so Input + CacheRead + CacheWrite is the prompt.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
}

// Prompt is the size of the prompt the step was built from.
func (u *Usage) Prompt() int {
	if u == nil {
		return 0
	}
	return u.Input + u.CacheRead + u.CacheWrite
}

// Header is the JSON line before every body. Fields beyond the first six
// belong to particular kinds and are omitted otherwise.
type Header struct {
	N    int       `json:"n"`
	Kind Kind      `json:"kind"`
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Len  int       `json:"len"`
	Type string    `json:"type,omitempty"`

	Step     string `json:"step,omitempty"`     // text, thinking, call: the step that closes it
	Provider string `json:"provider,omitempty"` // thinking: who signed it. step: who served it

	For        string `json:"for,omitempty"`         // result, error: the record it answers
	Exit       *int   `json:"exit,omitempty"`        // result of run
	Truncated  bool   `json:"truncated,omitempty"`   // result
	FullOutput string `json:"full_output,omitempty"` // result: path in worker scratch

	Via            string `json:"via,omitempty"`             // message: connector name. error: who stopped the session
	IdempotencyKey string `json:"idempotency_key,omitempty"` // message: the poster's key; a repost with it appends nothing
	Source         string `json:"source,omitempty"`          // system: where the text came from

	Upto    int             `json:"upto,omitempty"`    // step: last record in its context
	Model   string          `json:"model,omitempty"`   // step
	Harness string          `json:"harness,omitempty"` // step: version that built the request
	Request string          `json:"request,omitempty"` // step: sha256 of the request bytes
	Sent    time.Time       `json:"sent,omitzero"`     // step: when the request that served it went out; cache ttls count from here
	Usage   *Usage          `json:"usage,omitempty"`   // step
	Cache   json.RawMessage `json:"cache,omitempty"`   // step: provider cache markers
}

type Record struct {
	Header
	Body []byte
	Note string // result: text the build renders after the body, derived from the file, never written
}

// Conf is the body of the config record, record 2, fixed for the session's
// life. Model is one provider/model string. ContextLimit is the token limit
// past which the harness refuses to step. Input is the media types read hands
// to the model as media; text is always text, and any other file is read as text.
// Params are fields for the model's vendor, set on every request as its
// envelope decides.
type Conf struct {
	Model        string                     `json:"model"`
	Tools        string                     `json:"tools"`
	Workers      []Worker                   `json:"workers"`
	ContextLimit int                        `json:"context_limit"`
	Input        []string                   `json:"input,omitempty"`
	Params       map[string]json.RawMessage `json:"params,omitempty"`
}

// Worker is one worker the session may use. Its drives are the harness
// config's: the session sees what the worker mounts.
type Worker struct {
	Name string `json:"name"`
}

func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ValidID is true for what NewID makes. Ids from outside become paths, so
// anything else is refused before it reaches the filesystem.
func ValidID(id string) bool {
	if len(id) != 8 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

var ErrTorn = errors.New("torn record")

// Encode is the bytes of one record: header line, body, newline.
func Encode(r Record) ([]byte, error) {
	r.Len = len(r.Body)
	h, err := json.Marshal(r.Header)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(h)
	b.WriteByte('\n')
	b.Write(r.Body)
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// Decode reads one record. io.EOF at a record boundary is returned as is.
// EOF anywhere inside a record is ErrTorn.
func Decode(br *bufio.Reader) (Record, error) {
	line, err := br.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return Record{}, io.EOF
		}
		return Record{}, ErrTorn
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(line, &r.Header); err != nil {
		return Record{}, fmt.Errorf("header: %w", err)
	}
	if r.Len < 0 {
		return Record{}, fmt.Errorf("header: len %d", r.Len)
	}
	r.Body = make([]byte, r.Len)
	if _, err := io.ReadFull(br, r.Body); err != nil {
		return Record{}, ErrTorn
	}
	if nl, err := br.ReadByte(); err != nil || nl != '\n' {
		return Record{}, ErrTorn
	}
	return r, nil
}
