// Package worker is the side of a worker that runs code and moves files for a
// harness over one connection. A frame is one JSON header line, len body
// bytes, and a newline: the state file record shape, so a frame can be read
// and logged the same way. The body is never escaped.
package worker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Result limits are opencode's: what current models handle in one result. The
// worker spills output over them to a file; the harness clips what it shows.
const (
	MaxBytes = 50 << 10
	MaxLines = 2000
)

// Kinds. The first four go harness to worker; the rest come back.
const (
	Run   = "run"
	Read  = "read"
	Write = "write"
	Kill  = "kill" // end a session's environments

	Hello  = "hello"  // first frame on a connection: what the worker is
	Output = "output" // run: one chunk, in order
	Result = "result" // run: exit code, path of the whole output when spilled
	Bytes  = "bytes"  // read: the file
	Done   = "done"   // write, kill
	Error  = "error"  // any request: text the model reads
)

type Header struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind"`
	Len  int    `json:"len"`

	Session string `json:"session,omitempty"` // run, kill: environments live per session
	Interp  string `json:"interp,omitempty"`  // run: empty means the worker's first
	Timeout int    `json:"timeout,omitempty"` // run: seconds before the environment is killed
	Path    string `json:"path,omitempty"`    // read, write

	Exit int    `json:"exit,omitempty"` // result
	Full string `json:"full,omitempty"` // result: file holding all the output, when over the limits

	Name    string   `json:"name,omitempty"`    // hello: what sessions call this worker
	OS      string   `json:"os,omitempty"`      // hello
	Arch    string   `json:"arch,omitempty"`    // hello
	Interps []string `json:"interps,omitempty"` // hello: first is the default
	Scratch string   `json:"scratch,omitempty"` // hello: where spilled output lands
}

type Frame struct {
	Header
	Body []byte
}

func Encode(w io.Writer, f Frame) error {
	f.Len = len(f.Body)
	h, err := json.Marshal(f.Header)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(h)
	b.WriteByte('\n')
	b.Write(f.Body)
	b.WriteByte('\n')
	_, err = w.Write(b.Bytes())
	return err
}

func Decode(br *bufio.Reader) (Frame, error) {
	line, err := br.ReadBytes('\n')
	if err != nil {
		return Frame{}, err
	}
	var f Frame
	if err := json.Unmarshal(line, &f.Header); err != nil {
		return Frame{}, fmt.Errorf("frame header: %w", err)
	}
	f.Body = make([]byte, f.Len)
	if _, err := io.ReadFull(br, f.Body); err != nil {
		return Frame{}, err
	}
	if nl, err := br.ReadByte(); err != nil || nl != '\n' {
		return Frame{}, fmt.Errorf("frame body: missing newline")
	}
	return f, nil
}
