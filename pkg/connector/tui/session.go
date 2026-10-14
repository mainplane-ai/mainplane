package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

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
	pollWait = "30s"
	retryIn  = 2 * time.Second
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
	creating   bool
}

func newSession(a *App, c connector.Client, id string) *session {
	s := &session{c: c, id: id, transcript: NewScrollbox(), input: NewInput()}
	s.transcript.Style = a.Theme.text()
	s.input.Placeholder, s.input.OnSubmit = "Ask anything", s.send
	s.box = inputBox(a, s.input)
	s.inputRow = Box(&Node{Dir: Row}, &Node{Grow: 1}, s.box, &Node{Grow: 1})
	s.body = &Node{Grow: 1, View: s.transcript}
	s.empty = []*Node{{Grow: 1}, {View: Wordmark{}}, {Fixed: 1}, {Grow: 1}}
	s.sidebar = &Node{Fixed: sidebarWidth, BG: a.Theme.Panel}
	main := Box(&Node{Dir: Col, Grow: 1},
		s.empty[0], s.empty[1], s.empty[2], s.body, s.inputRow, s.empty[3], &Node{Fixed: 1})
	s.root = Box(&Node{Dir: Row, BG: a.Theme.BG}, s.sidebar, main)
	a.Focus(s.box.Kids[1])
	s.title(a)
	if id != "" {
		s.follow(a)
	}
	return s
}

func (s *session) Root() *Node { return s.root }

func (s *session) Size(a *App, w, h int) {
	pad := 2
	if w < narrow {
		pad = 1
	}
	empty := s.msgs == 0
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
		case a.Again():
			s.stop(a)
		default:
			a.Toast("Esc again to stop")
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

// title names the tab after the session, with a spinner while it steps.
func (s *session) title(a *App) {
	t := "Mainplane"
	switch {
	case s.info.Name != "":
		t = s.info.Name
	case s.id != "":
		t = s.id
	}
	if s.stepping() {
		t = spinner[s.frame%len(spinner)] + " " + t
	}
	a.T.SetTitle(t)
}

// follow reads the records and the status, then long polls for more, until
// the app ends. Each batch lands on the loop with the status after it.
func (s *session) follow(a *App) {
	id := s.id
	a.Go(func() {
		ctx, after, wait := context.Background(), 0, ""
		for {
			recs, err := s.c.Records(ctx, id, after, wait)
			var info harness.Info
			if err == nil {
				info, err = s.c.Info(ctx, id)
			}
			if err != nil {
				a.Post(func() { a.ToastError(err) })
				time.Sleep(retryIn)
				continue
			}
			if len(recs) > 0 {
				after = recs[len(recs)-1].N
			}
			wait = pollWait
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
	s.info = info
	if s.stepping() != was {
		a.T.Progress(s.stepping())
		a.Ticking(s.stepping())
	}
	s.title(a)
}

// raw is a record as the file holds it: the header line, then the body. A
// body that is not text is its size and type, and control bytes are dots.
func raw(r statefile.Record) string {
	h, _ := json.Marshal(r.Header) // a decoded header marshals
	body := string(r.Body)
	if !strings.HasPrefix(r.Type, "text/") && r.Type != "application/json" && r.Type != "" {
		body = fmt.Sprintf("<%d bytes %s>", len(r.Body), r.Type)
	}
	return string(h) + "\n" + printable(body)
}

func printable(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\n' {
			return '.'
		}
		return c
	}, s)
}

// send posts the text. From home it first makes the session, with the
// config of the session updated last.
func (s *session) send(a *App, text string) {
	if s.id != "" {
		id := s.id
		a.Go(func() {
			if _, err := s.c.Post(context.Background(), id, via, text, nil); err != nil {
				a.Post(func() { a.ToastError(err) })
			}
		})
		return
	}
	if s.creating {
		return
	}
	s.creating = true
	a.Go(func() {
		id, err := s.create(text)
		a.Post(func() {
			s.creating = false
			if err != nil {
				a.ToastError(err)
			}
			if id == "" {
				s.input.Paste(a, text)
				return
			}
			s.id = id
			s.title(a)
			s.follow(a)
		})
	})
}

func (s *session) create(text string) (string, error) {
	ctx := context.Background()
	infos, err := s.c.Sessions(ctx, "", 1)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", errors.New("no session to take the config from: make one with mainplane new")
	}
	cf := infos[0].Config
	id, err := s.c.Create(ctx, harness.Create{Model: cf.Model, ContextLimit: cf.ContextLimit, Workers: cf.Workers, Input: cf.Input, Params: cf.Params}, via)
	if err != nil {
		return "", err
	}
	_, err = s.c.Post(ctx, id, via, text, nil)
	return id, err
}

func (s *session) stop(a *App) {
	id := s.id
	a.Toast("Stopping")
	a.Go(func() {
		if err := s.c.Stop(context.Background(), id, via); err != nil {
			a.Post(func() { a.ToastError(err) })
		}
	})
}
