package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// runTimeout bounds one run: long enough for a build or an install, and the
// most a hung command costs before its environment is reset.
const runTimeout = 10 * time.Minute

// Remote is a worker on the far end of a connection. Its hello header says
// what it is. Requests are multiplexed by id; a call waits for its terminal
// frame or the connection's end, not for ctx: the worker's timeout bounds it.
type Remote struct {
	worker.Header
	conn  net.Conn
	wmu   sync.Mutex
	cmu   sync.Mutex
	calls map[string]chan worker.Frame
	done  chan struct{} // closed when the connection ends
}

// Connect reads the worker's hello and starts routing its replies.
func Connect(conn net.Conn) (*Remote, error) {
	br := bufio.NewReader(conn)
	hello, err := worker.Decode(br)
	if err != nil {
		return nil, err
	}
	if hello.Kind != worker.Hello {
		return nil, fmt.Errorf("first frame is %q, want hello", hello.Kind)
	}
	r := &Remote{Header: hello.Header, conn: conn, calls: map[string]chan worker.Frame{}, done: make(chan struct{})}
	go r.recv(br)
	return r, nil
}

func (r *Remote) recv(br *bufio.Reader) {
	for {
		f, err := worker.Decode(br)
		r.cmu.Lock()
		if err != nil {
			for _, ch := range r.calls {
				close(ch)
			}
			r.calls = nil
			r.cmu.Unlock()
			_ = r.conn.Close()
			close(r.done)
			return
		}
		if ch, ok := r.calls[f.ID]; ok {
			ch <- f
		}
		r.cmu.Unlock()
	}
}

// call sends one request and feeds every reply to on until the terminal one.
func (r *Remote) call(h worker.Header, body []byte, on func(worker.Frame)) error {
	id := statefile.NewID()
	ch := make(chan worker.Frame)
	r.cmu.Lock()
	if r.calls == nil {
		r.cmu.Unlock()
		return errors.New("worker connection lost")
	}
	r.calls[id] = ch
	r.cmu.Unlock()
	defer func() {
		r.cmu.Lock()
		delete(r.calls, id)
		r.cmu.Unlock()
	}()
	h.ID = id
	r.wmu.Lock()
	err := worker.Encode(r.conn, worker.Frame{Header: h, Body: body})
	r.wmu.Unlock()
	if err != nil {
		return err
	}
	for f := range ch {
		if f.Kind == worker.Error {
			return errors.New(string(f.Body))
		}
		on(f)
		if f.Kind != worker.Output {
			return nil
		}
	}
	return errors.New("worker connection lost")
}

// Run streams the output and keeps a tail within the result limits. Past twice
// the byte limit the front is dropped at a line start and the lines counted,
// a line cut in the middle counting as one; the worker has the whole in the
// file the result names.
func (r *Remote) Run(_ context.Context, session, interpreter, code string) (Output, error) {
	var o Output
	mid := false // the tail starts inside a line already counted
	err := r.call(worker.Header{Kind: worker.Run, Session: session, Interp: interpreter, Timeout: int(runTimeout.Seconds())}, []byte(code), func(f worker.Frame) {
		if f.Kind == worker.Result {
			o.Exit, o.Full = f.Exit, f.Full
			return
		}
		o.Body = append(o.Body, f.Body...)
		if len(o.Body) > 2*worker.MaxBytes {
			drop := len(o.Body) - worker.MaxBytes
			if i := bytes.IndexByte(o.Body[drop:len(o.Body)-1], '\n'); i >= 0 {
				drop += i + 1
			}
			o.Cut += bytes.Count(o.Body[:drop], []byte("\n"))
			if mid {
				o.Cut--
			}
			if mid = o.Body[drop-1] != '\n'; mid {
				o.Cut++
			}
			o.Body = slices.Clone(o.Body[drop:])
		}
	})
	return o, err
}

func (r *Remote) Read(_ context.Context, path string) ([]byte, error) {
	var b []byte
	err := r.call(worker.Header{Kind: worker.Read, Path: path}, nil, func(f worker.Frame) { b = f.Body })
	return b, err
}

func (r *Remote) Write(_ context.Context, path string, data []byte) error {
	return r.call(worker.Header{Kind: worker.Write, Path: path}, data, func(worker.Frame) {})
}

func (r *Remote) Kill(_ context.Context, session string) error {
	return r.call(worker.Header{Kind: worker.Kill, Session: session}, nil, func(worker.Frame) {})
}
