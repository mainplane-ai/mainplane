package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const (
	// via is what this connector writes on every record it causes.
	via = "tui"
	// Layout breakpoints and sizes, opencode's, but the sidebar docks 10 columns later.
	sidebarFrom  = 131 // the sidebar docks at this width and wider
	sidebarWidth = 42
	narrow       = 44 // below this width: padding 1, no scrollbar
	// The input grows to inputRows or inputShare percent of the height.
	inputRows  = 10
	inputShare = 40
	// pollWait is how long one long poll of the records waits.
	pollWait = 30 * time.Second
	// callTime bounds every call but the long poll, which gets it on top.
	callTime = 15 * time.Second
	// signinTime bounds a login, which the harness ends after 15 minutes.
	signinTime = 16 * time.Minute
	retryIn    = 2 * time.Second
	// escTwice is how close the two presses of Esc Esc, stop, must be.
	escTwice = time.Second
	// outboxSize is how many sends can wait to post before a send blocks the loop.
	outboxSize = 64
)

// session is one session: the transcript, the input, the status line, and a
// sidebar when wide. With no message yet it is the wordmark above the input.
// With no id it is home: the first message makes the session.
type session struct {
	c      connector.Client
	list   *sessionList
	id     string
	info   harness.Info
	asked  []*msgBlock // the messages, for /rollback and /goto
	second int64       // the clock's second at the last tick, to redraw the times of running calls

	transcript *Scrollbox
	input      *Input
	root       *Node
	sidebar    *Node
	body       *Node // the transcript's node
	empty      []*Node
	inputRow   *Node
	slash      *slash
	slashBox   *Node // the list of commands, as wide as the input box
	slashRow   *Node // above the input row, which it takes the blank row of
	box        *Node
	status     *Text   // the app's status, else the model
	meta       *Glyphs // context, cost, and the commands key
	statusRow  *Node
	busy       *Glyphs // working, stopping, or making the session, over the input
	busyRow    *Node
	armed      bool            // Esc was pressed once: again stops
	turn       time.Time       // the last message, from which the work is timed
	cfg        *statefile.Conf // the config to make the session with, else the default
	home       *statefile.Conf // on home with none chosen, the default, to show under the input
	homeAsked  bool            // home is fetched, or being fetched, for the providers there are
	blank      bool            // no message yet: the wordmark, and the plane behind it
	plane      *plane
	price      *catalog.Cost   // the price set in the config's JSON, else the catalog's
	ctx        context.Context // ends when the screen leaves the session
	cancel     context.CancelFunc
	started    bool                  // a message was sent from home: show the transcript
	creating   bool                  // the session is being made
	pending    []string              // sent while the session is being made
	outbox     chan string           // sent, to be posted in order
	stopping   bool                  // a stop was asked for and the status has not changed since
	calls      map[string]*callBlock // by call id, to pair each result with its call
	running    []*callBlock          // calls with no result yet
	workerSet  sync.Mutex            // one PUT of the workers at a time
	workerSets atomic.Int64          // the toggles so far; only the newest is sent
}

func newSession(a *App, l *sessionList, id string) *session {
	s := &session{
		c: l.c, list: l, id: id, transcript: NewScrollbox(), input: NewInput(), status: &Text{}, outbox: make(chan string, outboxSize), calls: map[string]*callBlock{},
		meta: &Glyphs{}, busy: &Glyphs{},
	}
	s.transcript.Gap = func(prev, next Block) bool {
		_, p := prev.(*callBlock)
		_, n := next.(*callBlock)
		return !p || !n
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if l.cur == nil {
		l.cur = s
	}
	s.input.Placeholder, s.input.OnSubmit = "Ask anything", s.send
	s.slash = &slash{s: s, in: s.input, all: commands()}
	s.input.Before = s.slash.Key
	s.box = inputBox(a, s.input)
	s.inputRow = Box(&Node{Dir: Row}, &Node{Grow: 1}, s.box, &Node{Grow: 1})
	s.slashBox = Box(&Node{Dir: Row}, &Node{Fixed: 1, View: bar{a.Theme.Muted}}, &Node{Grow: 1, BG: a.Theme.Panel, View: s.slash})
	s.slashRow = Box(&Node{Dir: Row}, &Node{Grow: 1}, s.slashBox, &Node{Grow: 1})
	s.body = &Node{Grow: 1, View: s.transcript}
	s.empty = []*Node{{Grow: 1}, {View: Wordmark{}}, {Fixed: 1}, {Grow: 1}}
	s.statusRow = Box(&Node{Dir: Row, Min: 1, Max: 3}, &Node{Grow: 1, View: s.status}, &Node{View: s.meta})
	s.busyRow = &Node{Fixed: 2, Pad: [4]int{0, 1, 0, 0}, View: s.busy}
	s.plane = &plane{on: func() bool { return s.blank }}
	s.sidebar = &Node{Fixed: sidebarWidth, BG: a.Theme.Panel, Pad: [4]int{0, 1, 0, 1}, View: l}
	main := Box(&Node{Dir: Col, Grow: 1, Over: s.plane},
		s.empty[0], s.empty[1], s.empty[2], s.body, s.busyRow, s.slashRow, s.inputRow, s.statusRow, &Node{Fixed: 1}, s.empty[3]) // a blank row under the status, as opencode
	s.root = Box(&Node{Dir: Row, BG: a.Theme.BG}, s.sidebar, main)
	a.Focus(s.box.Kids[1])
	if d, ok := l.drafts[id]; ok {
		s.input.els, s.input.cur = append([]elem(nil), d...), len(d)
	}
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
	loaded := s.id != "" && s.info.ID == s.id
	empty := !s.started && (s.id == "" || loaded && len(s.asked) == 0) // a session that loads shows its blank transcript, not home
	s.blank, a.Frame = empty, 0
	if empty && !a.Blurred {
		a.Frame = planeFrame
	}
	s.list.tick(a)
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
	s.slash.update()
	s.slashRow.Hidden = len(s.slash.cmds) == 0
	s.slashRow.Pad = s.inputRow.Pad
	s.slashRow.Kids[0].Hidden, s.slashRow.Kids[2].Hidden = !empty, !empty
	s.slashBox.Fixed, s.slashBox.Grow = s.box.Fixed, s.box.Grow
	if !s.slashRow.Hidden {
		s.inputRow.Pad[1] = 0
	}
	s.statusRow.Pad = [4]int{pad + 2, 0, pad, 0} // under the input's text, to the box's right edge
	if empty {
		left := pad + (main-2*pad-s.box.Fixed)/2
		s.statusRow.Pad = [4]int{left + 2, 0, main - left - s.box.Fixed, 0}
		s.askHome(a)
	}
	cf := s.info.Config
	if cf.Model == "" && s.cfg != nil {
		cf = *s.cfg
	}
	if cf.Model == "" && empty && s.home != nil {
		cf = *s.home
	}
	*s.status = *a.Status
	if s.status.S == "" {
		s.status.S, s.status.Style = cf.Model, a.Theme.muted()
	}
	s.meta.Gs = nil
	switch {
	case !empty:
		s.meta.Gs = s.metaLine(a, main-2*pad > hintFrom)
	case cf.ContextLimit > 0:
		s.meta.Gs = run("0/"+tokens(cf.ContextLimit), a.Theme.muted())
	}
	s.statusRow.Kids[1].Fixed = gwidth(s.meta.Gs)
	s.statusRow.Kids[1].Hidden = len(s.meta.Gs) == 0
	s.busy.Gs = s.busyLine(a)
	s.busyRow.Hidden = len(s.busy.Gs) == 0 || empty
	s.busyRow.Pad[0] = pad + margin
}

// askHome fetches the default config for home, once the catalog and the
// sessions it takes its preset from are loaded. With no provider there is
// none to show; the send says why.
func (s *session) askHome(a *App) {
	if s.id != "" || s.cfg != nil || s.homeAsked || s.list.cat == nil || !s.list.loaded {
		return
	}
	s.homeAsked = true
	s.withProviders(a, func(cat *catalog.Catalog, ps []harness.ProviderInfo) {
		if d, err := defaultConf(cat, ps, s.list.infos); err == nil {
			s.home = &d
		}
	})
}

// hintFrom is the width of the status line from which it shows the commands key.
const hintFrom = 60

// metaLine is the right of the status line: the context used of its limit,
// the cost once the catalog is loaded, and the commands key.
func (s *session) metaLine(a *App, hint bool) []Glyph {
	muted := a.Theme.muted()
	key := term.Style{FG: a.Theme.Text, Attr: term.Bold}
	var parts [][]Glyph
	if s.id != "" && s.info.ID == s.id {
		c := tokens(s.info.ContextUsed)
		if l := s.info.Config.ContextLimit; l > 0 {
			c += "/" + tokens(l)
		}
		parts = append(parts, run(c, muted))
		if c, ok := s.cost(); ok {
			parts = append(parts, run(fmt.Sprintf("$%.2f", c), muted))
		}
	}
	if hint {
		parts = append(parts, cat(run("ctrl+p", key), run(" commands", muted)))
	}
	var gs []Glyph
	for i, p := range parts {
		if i > 0 {
			gs = append(gs, run(" · ", muted)...)
		}
		gs = append(gs, p...)
	}
	return gs
}

// tokens is a count of tokens in thousands.
func tokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa((n+500)/1000) + "k"
}

// cost is the session's cost so far, once the catalog is loaded.
func (s *session) cost() (float64, bool) {
	switch {
	case s.price != nil:
		return costOf(*s.price, s.info.Usage), true
	case s.list.cat == nil:
		return 0, false
	}
	return costOf(price(s.list.cat, s.info.Config), s.info.Usage), true
}

// busyLine is what the session is busy with, over the input, or nothing.
func (s *session) busyLine(a *App) []Glyph {
	muted := a.Theme.muted()
	key := term.Style{FG: a.Theme.Text, Attr: term.Bold}
	switch {
	case s.creating:
		return run("Making the session...", muted)
	case s.stopping:
		return run("Stopping...", muted)
	case !s.stepping():
		return nil
	}
	gs := run(Spin()+" Working", muted)
	if !s.turn.IsZero() {
		gs = append(gs, run(" "+dur(time.Since(s.turn).Truncate(time.Second)), muted)...)
	}
	again := ""
	if s.armed {
		again = " again"
	}
	return cat(gs, run("  ", muted), run("esc", key), run(again+" to stop", muted))
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
			s.armed = true
			time.AfterFunc(escTwice, func() { a.Post(func() { s.armed = false }) })
		}
	case "ctrl+p", "f1":
		s.palette(a)
	case "ctrl+up":
		s.list.step(a, -1)
	case "ctrl+down":
		s.list.step(a, 1)
	default:
		for _, c := range commands() {
			if c.key == k.String() {
				c.run(s, a)
				return true
			}
		}
		return false
	}
	return true
}

func (s *session) Tick(a *App) {
	if sec := time.Now().Unix(); sec != s.second {
		s.second = sec
		for _, b := range s.running {
			s.transcript.Changed(b)
		}
	}
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
		t = Spin() + " " + t
	}
	a.T.SetTitle(t)
}

// follow reads the records and the status, then long polls for more, until
// the app ends. Each batch lands on the loop with the status after it.
func (s *session) follow(a *App) {
	id := s.id
	a.Go(func() {
		after, wait := 0, ""
		for s.ctx.Err() == nil {
			ctx, cancel := context.WithTimeout(s.ctx, pollWait+callTime)
			recs, err := s.c.Records(ctx, id, after, wait)
			var info harness.Info
			if err == nil {
				info, err = s.c.Info(ctx, id)
			}
			cancel()
			if s.ctx.Err() != nil {
				return
			}
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
		s.show(r)
	}
	was := s.stepping()
	if info.Status != s.info.Status {
		s.stopping = false
	}
	s.info = info
	if s.stepping() != was {
		a.T.Progress(s.stepping())
		s.list.tick(a)
		for _, b := range s.running {
			s.transcript.Changed(b)
		}
	}
	s.title(a)
}

// show puts a record in the transcript, or a result in its call's block.
func (s *session) show(r statefile.Record) {
	switch r.Kind {
	case statefile.Start, statefile.Config, statefile.System, statefile.Name, statefile.Thinking, statefile.Step:
	case statefile.Message:
		b := &msgBlock{r}
		s.asked = append(s.asked, b)
		s.turn = r.Time
		s.transcript.Append(b)
	case statefile.Text:
		s.transcript.Append(&textBlock{string(r.Body)})
	case statefile.Error:
		s.transcript.Append(&errBlock{string(r.Body)})
	case statefile.Call:
		b := newCall(s, r)
		s.calls[r.ID] = b
		s.running = append(s.running, b)
		s.transcript.Append(b)
	case statefile.Result:
		b, ok := s.calls[r.For]
		if !ok {
			s.transcript.Append(&callBlock{s: s, res: &r})
			return
		}
		b.res = &r
		s.transcript.Changed(b)
		delete(s.calls, r.For)
		for i, x := range s.running {
			if x == b {
				s.running = append(s.running[:i], s.running[i+1:]...)
				break
			}
		}
	}
}

// send posts the text. From home the screen turns to the session at once;
// the session is made in the background and the text posted when it is.
func (s *session) send(a *App, text string) {
	s.started = true
	if s.id != "" {
		s.outbox <- text
		return
	}
	s.pending = append(s.pending, text)
	if !s.creating {
		s.create(a)
	}
}

// postAll starts the one goroutine that posts texts, then what is sent, in
// the order it was sent. What was sent before the screen left the session is
// still posted. A text that fails goes back to the input.
func (s *session) postAll(a *App, texts ...string) {
	id := s.id
	a.Go(func() {
		for {
			var text string
			if len(texts) > 0 {
				text, texts = texts[0], texts[1:]
			} else {
				select {
				case text = <-s.outbox:
				case <-s.ctx.Done():
					select {
					case text = <-s.outbox:
					default:
						return
					}
				}
			}
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

// create makes the session with the config chosen, else the default one,
// then follows it and posts what was sent meanwhile. If it fails, the screen
// is home again with the text in the input.
func (s *session) create(a *App) {
	s.creating = true
	cf, cat := s.cfg, s.list.cat
	a.Go(func() {
		id, err := s.open(cf, cat)
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
			s.postAll(a, texts...)
		})
	})
}

// open makes the session with cf, else with defaultConf.
func (s *session) open(cf *statefile.Conf, cat *catalog.Catalog) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTime)
	defer cancel()
	if cf == nil {
		if cat == nil {
			return "", errors.New("the catalog is not loaded")
		}
		infos, err := s.c.Sessions(ctx, "", 1)
		if err != nil {
			return "", err
		}
		ps, err := s.c.Providers(ctx)
		if err != nil {
			return "", err
		}
		d, err := defaultConf(cat, ps, infos)
		if err != nil {
			return "", err
		}
		cf = &d
	}
	return s.c.Create(ctx, harness.Create{Model: cf.Model, Tools: cf.Tools, ContextLimit: cf.ContextLimit, Workers: cf.Workers, Input: cf.Input, Params: cf.Params, Preset: cf.Preset}, via)
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
