package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// OpenAI's Responses API takes a step over a WebSocket too:
// https://developers.openai.com/api/docs/guides/websocket-mode. The server
// keeps the last response of the socket in memory, so a step that continues it
// sends only the input past it, chained by previous_response_id, even with
// store false. The step record's request hash stays the full body's: the
// delta is transport only.
//
// The server ends a socket at 60 minutes; socketLife opens a new one before,
// so a step does not fail on the limit.
const socketLife = 55 * time.Minute

// Conn is a session's socket, kept from step to step while the session steps.
// The zero value has none; a failed step closes it, and the next step opens a
// new one and sends its input whole. A session steps one at a time, so Conn
// has no lock.
type Conn struct {
	ws     *websocket.Conn
	url    string
	header http.Header // the handshake's: auth that changes, such as a refreshed token or another provider, needs a new socket
	opened time.Time
	fields []byte            // the last request without its input and stream
	input  []json.RawMessage // what the server holds as the last response: its input, then its output compiled as input
	id     string            // the last response's id
}

// Close ends the socket.
func (c *Conn) Close() {
	if c.ws != nil {
		_ = c.ws.CloseNow()
	}
	*c = Conn{}
}

// socket is one attempt over the session's socket, the WebSocket twin of once.
// A continuation that fails is not httpError, so it is retried, whole, on a
// new socket: the server may have dropped the response it chained from.
func (p Provider) socket(ctx context.Context, c *Conn, url, session string, body []byte, emit func(statefile.Record)) (h statefile.Header, emitted bool, err error) {
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return h, false, err
	}
	var input []json.RawMessage
	if err := json.Unmarshal(fields["input"], &input); err != nil {
		return h, false, err
	}
	delete(fields, "input")
	delete(fields, "stream") // the socket streams; the docs leave transport fields out
	same, err := marshal(fields)
	if err != nil {
		return h, false, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return h, false, err
	}
	if err := p.header(ctx, hr, session); err != nil {
		return h, false, err
	}
	if c.ws != nil && (c.url != url || !maps.EqualFunc(c.header, hr.Header, slices.Equal) || time.Since(c.opened) > socketLife) {
		c.Close()
	}
	if c.ws == nil {
		if err := c.dial(ctx, p.Envelope.Name(), url, hr.Header); err != nil {
			return h, false, err
		}
	}
	send, chained := input, c.id != "" && bytes.Equal(same, c.fields) && len(input) >= len(c.input) &&
		slices.EqualFunc(input[:len(c.input)], c.input, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) })
	if chained {
		send = input[len(c.input):]
		if fields["previous_response_id"], err = marshal(c.id); err != nil {
			return h, false, err
		}
	}
	fields["type"] = json.RawMessage(`"response.create"`)
	if fields["input"], err = marshal(send); err != nil {
		return h, false, err
	}
	frame, err := marshal(fields)
	if err != nil {
		return h, false, err
	}
	wctx, cancel := context.WithTimeout(ctx, idle)
	defer cancel()
	if err := c.ws.Write(wctx, websocket.MessageText, frame); err != nil {
		return h, false, err
	}
	name := p.Envelope.Name()
	var out []statefile.Record
	tagged := func(r statefile.Record) {
		emitted = true
		if r.Kind == statefile.Thinking {
			r.Provider = name
		}
		out = append(out, r)
		emit(r)
	}
	var s respStream
	for !s.done {
		if err := c.read(ctx, name, chained, &s, tagged); err != nil {
			return s.header(), emitted, err
		}
	}
	items, err := openai{}.assistant(out)
	if err != nil {
		return h, emitted, err
	}
	for _, it := range items {
		b, err := marshal(it)
		if err != nil {
			return h, emitted, err
		}
		input = append(input, b)
	}
	c.fields, c.input, c.id = same, input, s.id
	return s.header(), emitted, nil
}

// dial opens the socket with the headers a request over HTTP has. A refused
// upgrade is httpError, so a 401 is not retried and a 429 or 5xx is.
func (c *Conn) dial(ctx context.Context, name, url string, header http.Header) error {
	ctx, cancel := context.WithTimeout(ctx, idle)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			msg, _ := io.ReadAll(resp.Body)
			return &httpError{name: name, status: resp.Status, code: resp.StatusCode, body: msg, after: retryAfter(resp.Header)}
		}
		return err
	}
	ws.SetReadLimit(maxEvent)
	c.ws, c.url, c.header, c.opened = ws, url, header, time.Now()
	return nil
}

// read takes one event. idle bounds the wait for it. An error event with a
// status is httpError, as the same refusal over HTTP would be, unless it
// answers a continuation.
func (c *Conn) read(ctx context.Context, name string, chained bool, s *respStream, emit func(statefile.Record)) error {
	ctx, cancel := context.WithTimeout(ctx, idle)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: no event for %s", name, idle)
	}
	if err != nil {
		return err
	}
	var e struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	if e.Type == "error" && e.Status != 0 && !chained {
		return &httpError{name: name, status: fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status)), code: e.Status, body: data}
	}
	return s.event(data, emit)
}
