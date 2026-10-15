package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const (
	// via is what this connector writes on every record it causes.
	via = "tui"
	// Layout breakpoints and sizes, opencode's.
	sidebarFrom  = 121 // the sidebar docks at this width and wider
	sidebarWidth = 42
	narrow       = 44 // below this width: padding 1, no scrollbar
	// The input grows to inputRows or inputShare percent of the height.
	inputRows  = 10
	inputShare = 40
	// pollWait is how long one long poll of the records waits.
	pollWait = 30 * time.Second
	// callTime bounds every call but the long poll, which gets it on top.
	callTime = 15 * time.Second
	retryIn  = 2 * time.Second
	// escTwice is how close the two presses of Esc Esc, stop, must be.
	escTwice = time.Second
	// outboxSize is how many sends can wait to post before a send blocks the loop.
	outboxSize = 64
)

// session is one session: the transcript, the input, the status line, and a
// sidebar when wide. With no message yet it is the wordmark above the input.
// With no id it is home: the first message makes the session.
type session struct {
	c     connector.Client
	id    string
	info  harness.Info
	msgs  int
	frame int

	transcript *Scrollbox
	input      *Input
	root       *Node
	sidebar    *Node
	body       *Node // the transcript's node
	empty      []*Node
	inputRow   *Node
	box        *Node
	status     *Text // the app's status, else what the session is busy with
	statusRow  *Node
	started    bool        // a message was sent from home: show the transcript
	creating   bool        // the session is being made
	pending    []string    // sent while the session is being made
	outbox     chan string // sent, to be posted in order
	stopping   bool        // a stop was asked for and the status has not changed since
}

func newSession(a *App, c connector.Client, id string) *session {
	s := &session{c: c, id: id, transcript: NewScrollbox(), input: NewInput(), status: &Text{}, outbox: make(chan string, outboxSize)}
	s.transcript.Style = a.Theme.text()
	s.input.Placeholder, s.input.OnSubmit = "Ask anything", s.send
	s.box = inputBox(a, s.input)
	s.inputRow = Box(&Node{Dir: Row}, &Node{Grow: 1}, s.box, &Node{Grow: 1})
	s.body = &Node{Grow: 1, View: s.transcript}
	s.empty = []*Node{{Grow: 1}, {View: Wordmark{}}, {Fixed: 1}, {Grow: 1}}
	s.statusRow = &Node{View: s.status, Min: 1, Max: 3}
	s.sidebar = &Node{Fixed: sidebarWidth, BG: a.Theme.Panel}
	main := Box(&Node{Dir: Col, Grow: 1},
		s.empty[0], s.empty[1], s.empty[2], s.body, s.inputRow, s.statusRow, &Node{Fixed: 1}, s.empty[3]) // a blank row under the status, as opencode
	s.root = Box(&Node{Dir: Row, BG: a.Theme.BG}, s.sidebar, main)
	a.Focus(s.box.Kids[1])
	s.title(a)
	if id != "" {
		s.follow(a)
		s.postAll(a)
	}
	return s
}

func (s *session) Root() *Node { return s.root }

func (s *session) Size(a *App, w, h int) {
	pad := 2
	if w < narrow {
		pad = 1
	}
	empty := s.msgs == 0 && !s.started
	s.sidebar.Hidden = w < sidebarFrom
	s.body.Hidden = empty
	for _, n := range s.empty {
		n.Hidden = !empty
	}
	s.body.Pad = [4]int{pad, 1, pad, 0}
	s.transcript.Bar = w >= narrow
	s.input.MaxRows = max(1, min(inputRows, h*inputShare/100))
	main := w
	if !s.sidebar.Hidden {
		main -= sidebarWidth
	}
	s.inputRow.Pad = [4]int{pad, 1, pad, 0}
	s.inputRow.Kids[0].Hidden, s.inputRow.Kids[2].Hidden = !empty, !empty
	s.box.Fixed, s.box.Grow = 0, 1
	if empty {
		s.box.Fixed, s.box.Grow = min(boxWidth, main-2*pad), 0
	}
	s.statusRow.Pad = [4]int{pad + 2, 0, pad, 0} // under the input's text
	if empty {
		s.statusRow.Pad[0] = pad
	}
	*s.status = *a.Status
	if s.status.S == "" {
		s.status.Style = a.Theme.muted()
		switch {
		case s.creating:
			s.status.S = "Making the session..."
		case s.stopping:
			s.status.S = "Stopping..."
		}
	}
	s.status.Center = empty
}

func (s *session) stepping() bool { return s.info.Status == statefile.StatusStepping }

func (s *session) Key(a *App, k term.Key) bool {
	switch k.String() {
	case "pgup":
		s.transcript.Scroll(-s.transcript.Page())
	case "pgdn":
		s.transcript.Scroll(s.transcript.Page())
	case "ctrl+home":
		s.transcript.Scroll(-1 << 30)
	case "ctrl+end":
		s.transcript.Scroll(1 << 30)
	case "esc":
		switch {
		case !s.stepping():
		case a.Again(escTwice):
			s.stop(a)
		default:
			const armed = "Esc again to stop"
			a.Say(armed)
			time.AfterFunc(escTwice, func() {
				a.Post(func() {
					if a.Status.S == armed {
						a.Status.S = ""
					}
				})
			})
		}
	case "f1":
		s.keys(a)
	default:
		return false
	}
	return true
}

// keys is the help dialog: every key of the session screen.
func (s *session) keys(a *App) {
	a.Dialog("Keys", &Node{View: &List{Items: []string{
		"enter          send",
		"shift+enter    new line (ctrl+j)",
		"shift+arrows   select in the input",
		"ctrl+x         cut the selection",
		"ctrl+delete    delete the next word",
		"home / end     start / end of the line",
		"pgup / pgdn    scroll the transcript",
		"ctrl+home/end  top / bottom of the transcript",
		"esc esc        stop the session",
		"ctrl+c         copy the selection, else exit",
		"ctrl+c ctrl+c  exit",
	}}})
}

func (s *session) Tick(a *App) {
	s.frame++
	s.title(a)
}

// title names the tab after the session, with a spinner while it steps
// where the terminal has no progress indicator of its own.
func (s *session) title(a *App) {
	t := "Mainplane"
	switch {
	case s.info.Name != "":
		t = s.info.Name
	case s.id != "":
		t = s.id
	}
	if s.stepping() && !a.T.Caps.Progress {
		t = spinner[s.frame%len(spinner)] + " " + t
	}
	a.T.SetTitle(t)
}

// follow reads the records and the status, then long polls for more, until
// the app ends. Each batch lands on the loop with the status after it.
func (s *session) follow(a *App) {
	id := s.id
	a.Go(func() {
		after, wait := 0, ""
		for {
			ctx, cancel := context.WithTimeout(context.Background(), pollWait+callTime)
			recs, err := s.c.Records(ctx, id, after, wait)
			var info harness.Info
			if err == nil {
				info, err = s.c.Info(ctx, id)
			}
			cancel()
			if err != nil {
				a.Post(func() { a.SayError(err) })
				time.Sleep(retryIn)
				continue
			}
			if len(recs) > 0 {
				after = recs[len(recs)-1].N
			}
			wait = pollWait.String()
			a.Post(func() { s.add(a, recs, info) })
		}
	})
}

func (s *session) add(a *App, recs []statefile.Record, info harness.Info) {
	for _, r := range recs {
		s.transcript.Append(raw(r))
		if r.Kind == statefile.Message {
			s.msgs++
		}
	}
	was := s.stepping()
	if info.Status != s.info.Status {
		s.stopping = false
	}
	s.info = info
	if s.stepping() != was {
		a.T.Progress(s.stepping())
		a.Ticking(s.stepping() && !a.T.Caps.Progress)
	}
	s.title(a)
}

// raw is a record as the file holds it: the header line, then the body. A
// body that is not text is its size and type.
func raw(r statefile.Record) string {
	h, _ := json.Marshal(r.Header) // a decoded header marshals
	body := string(r.Body)
	mt, _, _ := mime.ParseMediaType(r.Type)
	if !strings.HasPrefix(mt, "text/") && mt != "application/json" && r.Type != "" {
		body = fmt.Sprintf("<%d bytes %s>", len(r.Body), r.Type)
	}
	return string(h) + "\n" + strings.ReplaceAll(body, "\r\n", "\n")
}

// send posts the text. From home the screen turns to the session at once;
// the session is made in the background and the text posted when it is.
func (s *session) send(a *App, text string) {
	if s.id != "" {
		s.outbox <- text
		return
	}
	s.started = true
	s.pending = append(s.pending, text)
	if !s.creating {
		s.create(a)
	}
}

// postAll starts the one goroutine that posts what is sent, in the order it
// was sent. A text that fails goes back to the input.
func (s *session) postAll(a *App) {
	id := s.id
	a.Go(func() {
		for text := range s.outbox {
			ctx, cancel := context.WithTimeout(context.Background(), callTime)
			_, err := s.c.Post(ctx, id, via, text, nil)
			cancel()
			if err != nil {
				a.Post(func() { s.failed(a, text, err) })
			}
		}
	})
}

// failed says why a message did not post and gives it back to the input,
// unless something new was typed there.
func (s *session) failed(a *App, text string, err error) {
	a.SayError(err)
	if len(s.input.els) == 0 {
		s.input.Paste(a, text)
	}
}

// create makes the session with the config of the session updated last,
// then follows it and posts what was sent meanwhile. If it fails, the screen
// is home again with the text in the input.
func (s *session) create(a *App) {
	s.creating = true
	a.Go(func() {
		id, err := s.open()
		a.Post(func() {
			s.creating = false
			texts := s.pending
			s.pending = nil
			if err != nil {
				s.started = false
				s.failed(a, strings.Join(texts, "\n"), err)
				return
			}
			s.id = id
			s.title(a)
			s.follow(a)
			s.postAll(a)
			for _, t := range texts {
				s.outbox <- t
			}
		})
	})
}

func (s *session) open() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTime)
	defer cancel()
	infos, err := s.c.Sessions(ctx, "", 1)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", errors.New("no session to take the config from: make one with mainplane new")
	}
	cf := infos[0].Config
	return s.c.Create(ctx, harness.Create{Model: cf.Model, ContextLimit: cf.ContextLimit, Workers: cf.Workers, Input: cf.Input, Params: cf.Params}, via)
}

// stop asks the harness to stop. The status line says so until the status
// changes; the stop itself is a record in the transcript.
func (s *session) stop(a *App) {
	id := s.id
	s.stopping = true
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		if err := s.c.Stop(ctx, id, via); err != nil {
			a.Post(func() {
				s.stopping = false
				a.SayError(err)
			})
		}
	})
}
