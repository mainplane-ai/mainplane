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
	Link     Kind = "link"
	Config   Kind = "config"
	System   Kind = "system"
	Message  Kind = "message"
	Text     Kind = "text"
	Thinking Kind = "thinking"
	Call     Kind = "call"
	Step     Kind = "step"
	Result   Kind = "result"
	Summary  Kind = "summary"
	Error    Kind = "error"
)

type Mode string

const (
	Continue Mode = "continue"
	Restart  Mode = "restart"
)

// Position names a record in a state file. A prefix of an append-only file
// never changes, so a position is a value.
type Position struct {
	Session string `json:"session"`
	File    int    `json:"file"`
	N       int    `json:"n"`
}

type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
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

	From *Position `json:"from,omitempty"` // link
	Mode Mode      `json:"mode,omitempty"` // link

	Via    string `json:"via,omitempty"`    // message: connector name
	Source string `json:"source,omitempty"` // system: where the text came from

	Upto    int             `json:"upto,omitempty"`    // step: last record in its context
	Model   string          `json:"model,omitempty"`   // step
	Harness string          `json:"harness,omitempty"` // step: version that built the request
	Request string          `json:"request,omitempty"` // step: sha256 of the request bytes
	Usage   *Usage          `json:"usage,omitempty"`   // step
	Cache   json.RawMessage `json:"cache,omitempty"`   // step: provider cache markers
}

type Record struct {
	Header
	Body []byte
	// Seq is the index of the file within a loaded chain. Not stored.
	Seq int `json:"-"`
}

// Conf is the body of the config record: record 2 of every file.
type Conf struct {
	Model    string   `json:"model"`
	Provider string   `json:"provider"`
	Tools    string   `json:"tools"`
	Workers  []string `json:"workers"`
	History  string   `json:"history,omitempty"`
}

func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var errTorn = errors.New("torn record")

func encode(r Record) ([]byte, error) {
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

// decode reads one record. io.EOF at a record boundary is returned as is.
// EOF anywhere inside a record is errTorn.
func decode(br *bufio.Reader) (Record, error) {
	line, err := br.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return Record{}, io.EOF
		}
		return Record{}, errTorn
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(line, &r.Header); err != nil {
		return Record{}, fmt.Errorf("header: %w", err)
	}
	r.Body = make([]byte, r.Len)
	if _, err := io.ReadFull(br, r.Body); err != nil {
		return Record{}, errTorn
	}
	if nl, err := br.ReadByte(); err != nil || nl != '\n' {
		return Record{}, errTorn
	}
	return r, nil
}
