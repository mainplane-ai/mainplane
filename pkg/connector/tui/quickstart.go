package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// pollEvery is how often the quickstart reads the providers, workers and
// network: someone waits on each change, and the harness is on loopback.
const pollEvery = time.Second

// qsWidth is the widest the quickstart's column gets: an install line with
// its token fits on one row.
const qsWidth = 100

// The quickstart's words, all here, to be rewritten together.
const (
	qsTelemetry       = "Mainplane sends anonymous usage data. Set DO_NOT_TRACK=1 to turn it off."
	qsAnyKey          = "Press any key to get started"
	qsProvidersTitle  = "Connect a provider"
	qsProvidersLine   = "Agents need a model provider: an API key or a subscription."
	qsProvidersNone   = "No provider is connected yet."
	qsConnected       = "Connected: "
	qsWorkersTitle    = "Connect workers"
	qsWorkersLine     = "A worker is a machine the agents use. Run a line on each machine. A key or a click copies it."
	qsMacNote         = "A Mac asks on its own screen to Allow Network Volumes, Screen Recording and Accessibility."
	qsNetStarting     = " The network is starting. The lines work once it is ready."
	qsNetFailed       = "The network failed: "
	qsNoWorker        = "No worker is connected yet."
	qsConnectorsTitle = "Use Mainplane on another device"
	qsConnectorsLine  = "Run a line on the device. Then run `mainplane` there."
	qsMinting         = " Making an api key"
	qsKeyName         = "tui-%d" // the api key a quickstart mints, the first number free
	qsDrivesTitle     = "Share a drive"
	qsDrivesLine      = "A drive is a folder that the chosen workers all see. A server machine keeps it."
	qsDriveName       = "Name"
	qsDriveWorkers    = "Workers"
	qsDriveServer     = "Server machine"
	qsDriveAll        = "every worker, now and later"
	qsPrompt          = "Tell me what devices you have access to and what you can do"
	qsBack            = "Back"
	qsLater           = "Set up later"
	qsContinue        = "Continue"
	qsConnect         = "Connect a provider"
	qsUnix            = "linux, macos"
	qsWindows         = "windows"
)

// The quickstart's steps, in order. Each optional one has Set up later.
const (
	stepSplash = iota
	stepProviders
	stepWorkers
	stepConnectors
	stepDrives
)

// qsRow is a row to pick: a line to copy, or a choice to change.
type qsRow struct {
	label, text string
	do          func(a *App)
}

// qsButton is a button, greyed out and passed over when off.
type qsButton struct {
	label string
	on    bool
	do    func(a *App)
}

// quickstart is the first run on the harness's own machine: a provider,
// then workers, a connector and a drive, each but the provider to set up
// later, then the first session. It reads what it shows from the harness
// every pollEvery and stores nothing: the harness's state is the progress.
type quickstart struct {
	s      *session // the screen it ends on; its commands connect providers
	ctx    context.Context
	cancel context.CancelFunc
	step   int
	sel    int // over the rows, then the buttons; -1 until one is picked

	providers []harness.ProviderInfo
	workers   []connector.Worker
	network   string
	join      string
	key       string // the api key minted for a connector
	minting   bool
	drive     struct {
		name, server string
		workers      []string // nil is every worker
	}

	rows    []qsRow
	buttons []qsButton
	root    *Node
	col     *Node
	splash  *Node
	title   *Text
	body    *Text
	list    *qsRows
	note    *Text
	bar     *qsBar
	status  *Text
}

func newQuickstart(a *App, c connector.Client) *quickstart {
	q := &quickstart{
		s: newSession(a, newSessionList(a, c), ""), sel: -1, title: &Text{Style: term.Style{FG: a.Theme.Text, Attr: term.Bold}},
		body: &Text{Style: a.Theme.text()}, note: &Text{Style: a.Theme.muted()}, status: &Text{},
	}
	q.ctx, q.cancel = context.WithCancel(context.Background())
	q.list, q.bar = &qsRows{q: q}, &qsBar{q: q}
	q.splash = Box(&Node{Dir: Col},
		&Node{View: Wordmark{}}, &Node{Fixed: 1},
		&Node{View: &Text{S: qsTelemetry, Center: true, Style: a.Theme.muted()}}, &Node{Fixed: 1},
		&Node{View: &Text{S: qsAnyKey, Center: true, Style: a.Theme.text()}})
	q.col = Box(&Node{Dir: Col},
		&Node{View: q.title}, &Node{Fixed: 1},
		&Node{View: q.body}, &Node{Fixed: 1},
		&Node{View: q.list},
		&Node{View: q.note}, &Node{Fixed: 1},
		&Node{Fixed: 1, View: q.bar}, &Node{Fixed: 1},
		&Node{View: q.status})
	q.root = Box(&Node{Dir: Col, BG: a.Theme.BG}, &Node{Grow: 1}, q.splash, centered(q.col), &Node{Grow: 1})
	a.Focus(nil)
	q.poll(a)
	return q
}

// poll reads the providers, workers, network and join lines until the
// quickstart ends.
func (q *quickstart) poll(a *App) {
	c := q.s.c
	a.Go(func() {
		for q.ctx.Err() == nil {
			ctx, cancel := context.WithTimeout(q.ctx, callTime)
			ps, err := c.Providers(ctx)
			var ws []connector.Worker
			var network, join string
			if err == nil {
				ws, err = c.Workers(ctx)
			}
			if err == nil {
				network, err = c.Network(ctx)
			}
			if err == nil {
				join, err = c.Join(ctx)
			}
			cancel()
			a.Post(func() {
				if q.ctx.Err() != nil {
					return
				}
				if err != nil {
					a.SayError(err)
					return
				}
				if len(q.providers) == 0 && len(ps) > 0 {
					q.sel = -1 // on to Continue
				}
				q.providers, q.workers, q.network, q.join = ps, ws, network, join
			})
			select {
			case <-q.ctx.Done():
			case <-time.After(pollEvery):
			}
		}
	})
}

func (q *quickstart) Root() *Node { return q.root }

func (q *quickstart) Size(a *App, w, h int) {
	q.splash.Hidden, q.col.Hidden = q.step != stepSplash, q.step == stepSplash
	q.col.Fixed = min(qsWidth, w-4)
	q.layout(a)
	if q.sel >= len(q.rows)+len(q.buttons) || q.sel >= len(q.rows) && !q.buttons[q.sel-len(q.rows)].on {
		q.sel = -1
	}
	*q.status = *a.Status
	a.Ticking(q.minting || q.step == stepWorkers && q.network == "starting")
}

func (q *quickstart) Tick(*App) {}

// at is the row or button selected: until one is picked, the last button
// that is on, Continue when it is.
func (q *quickstart) at() int {
	if q.sel >= 0 {
		return q.sel
	}
	for i := len(q.buttons) - 1; i >= 0; i-- {
		if q.buttons[i].on {
			return len(q.rows) + i
		}
	}
	return -1
}

// layout is the step's words, rows and buttons, from what the harness said
// last.
func (q *quickstart) layout(a *App) {
	q.rows, q.note.S = nil, ""
	back := qsButton{qsBack, true, func(a *App) { q.to(q.step - 1) }}
	later := qsButton{qsLater, true, func(a *App) { q.next(a) }}
	switch q.step {
	case stepProviders:
		q.title.S, q.body.S = qsProvidersTitle, qsProvidersLine
		q.note.S = q.providerNote()
		q.buttons = []qsButton{back, {qsConnect, true, q.s.providers}, {qsContinue, len(q.providers) > 0, q.next}}
	case stepWorkers:
		q.title.S, q.body.S = qsWorkersTitle, qsWorkersLine
		q.lines(q.join)
		q.note.S = q.workerNote()
		q.buttons = []qsButton{back, later, {qsContinue, true, q.next}}
	case stepConnectors:
		q.title.S, q.body.S = qsConnectorsTitle, qsConnectorsLine
		q.lines(q.key)
		if q.key == "" && !q.minting {
			q.mint(a)
		}
		if q.minting {
			q.note.S = Spin() + qsMinting
		}
		q.buttons = []qsButton{back, later, {qsContinue, q.key != "", q.next}}
	case stepDrives:
		q.title.S, q.body.S = qsDrivesTitle, qsDrivesLine
		ws := qsDriveAll
		if q.drive.workers != nil {
			ws = strings.Join(q.drive.workers, ", ")
		}
		q.rows = []qsRow{{qsDriveName, q.drive.name, q.nameDialog}, {qsDriveWorkers, ws, q.workersMenu}, {qsDriveServer, q.drive.server, q.serverMenu}}
		q.buttons = []qsButton{back, later, {qsContinue, q.drive.name != "" && q.drive.server != "", q.saveDrive}}
	}
}

// providerNote names the providers connected, by the catalog's names.
func (q *quickstart) providerNote() string {
	if len(q.providers) == 0 {
		return qsProvidersNone
	}
	var names []string
	for _, p := range q.providers {
		name := p.Name
		if cat := q.s.list.cat; cat != nil && cat.Providers[name].Name != "" {
			name = cat.Providers[name].Name
		}
		names = append(names, name)
	}
	return qsConnected + strings.Join(names, ", ")
}

// workerNote is the network while it is not ready, the workers connected,
// and what a Mac asks.
func (q *quickstart) workerNote() string {
	var b strings.Builder
	switch q.network {
	case "ready":
	case "starting":
		b.WriteString(Spin() + qsNetStarting + "\n")
	default:
		b.WriteString(qsNetFailed + q.network + "\n")
	}
	var names []string
	for _, w := range q.workers {
		if w.Refused != "" {
			names = append(names, w.Name+" (refused: "+w.Refused+")")
			continue
		}
		names = append(names, w.Name)
	}
	if len(names) == 0 {
		b.WriteString(qsNoWorker)
	} else {
		b.WriteString(qsConnected + strings.Join(names, ", "))
	}
	b.WriteString("\n" + qsMacNote)
	return b.String()
}

// lines are the rows that run the install script with args, a row each for
// Linux and macOS and for Windows; none until args are known.
func (q *quickstart) lines(args string) {
	if args == "" {
		return
	}
	unix, windows := release.InstallLines(args)
	copyRow := func(label, text string) qsRow {
		return qsRow{label, text, func(a *App) { a.Copy(text) }}
	}
	q.rows = []qsRow{copyRow(qsUnix, unix), copyRow(qsWindows, windows)}
}

// servers are the connected workers that can serve a drive.
func (q *quickstart) servers() []string {
	var names []string
	for _, w := range q.workers {
		if w.Refused == "" && w.DriveServer {
			names = append(names, w.Name)
		}
	}
	return names
}

// to opens step n; the drives step only when a worker can serve one.
func (q *quickstart) to(n int) {
	if n == stepDrives && len(q.servers()) == 0 {
		n = stepConnectors
	}
	q.step, q.sel = n, -1
}

// next opens the step after this one, or, after the last, the session.
func (q *quickstart) next(a *App) {
	if q.step == stepDrives || q.step == stepConnectors && len(q.servers()) == 0 {
		q.finish(a)
		return
	}
	if q.step+1 == stepDrives && q.drive.server == "" {
		srv := q.servers()
		q.drive.server = srv[0]
		if slices.Contains(srv, "admin") {
			q.drive.server = "admin"
		}
	}
	q.to(q.step + 1)
}

// mint makes an api key for the connector lines, named with the first number
// free.
func (q *quickstart) mint(a *App) {
	q.minting = true
	c := q.s.c
	a.Go(func() {
		ctx, cancel := context.WithTimeout(q.ctx, callTime)
		defer cancel()
		var key string
		var err error
		for i := 1; ; i++ {
			key, err = c.NewKey(ctx, fmt.Sprintf(qsKeyName, i))
			var se *connector.StatusError
			if !errors.As(err, &se) || se.Code != http.StatusConflict {
				break
			}
		}
		a.Post(func() {
			q.minting = false
			if err != nil {
				a.SayError(err)
				return
			}
			q.key = key
		})
	})
}

func (q *quickstart) nameDialog(a *App) {
	ask(a, qsDriveName, "files", false, func(_ *App, name string) { q.drive.name = name })
}

// workersMenu checks the workers of the drive: every one, or those picked.
func (q *quickstart) workersMenu(a *App) {
	m := &Menu{Checks: true}
	m.Items = append(m.Items, MenuItem{Label: qsDriveAll, On: q.drive.workers == nil})
	for _, w := range q.workers {
		if w.Refused == "" {
			m.Items = append(m.Items, MenuItem{Label: w.Name, On: slices.Contains(q.drive.workers, w.Name)})
		}
	}
	set := func(_ *App, it *MenuItem) {
		all := it == &m.Items[0]
		if it.On {
			for i := range m.Items {
				if all != (i == 0) {
					m.Items[i].On = false
				}
			}
		}
		q.drive.workers = nil
		for _, x := range m.Items[1:] {
			if x.On {
				q.drive.workers = append(q.drive.workers, x.Label)
			}
		}
		if q.drive.workers == nil {
			m.Items[0].On = true
		}
	}
	for i := range m.Items {
		m.Items[i].Pick = set
	}
	a.OpenMenu(qsDriveWorkers, m)
}

func (q *quickstart) serverMenu(a *App) {
	m := &Menu{}
	for _, n := range q.servers() {
		m.Items = append(m.Items, MenuItem{Label: n, Pick: func(*App, *MenuItem) { q.drive.server = n }})
	}
	a.OpenMenu(qsDriveServer, m)
}

// saveDrive makes the drive, then opens the session.
func (q *quickstart) saveDrive(a *App) {
	name, server, ws := q.drive.name, q.drive.server, q.drive.workers
	if ws == nil {
		ws = []string{"*"}
	}
	c := q.s.c
	a.Go(func() {
		ctx, cancel := context.WithTimeout(q.ctx, callTime)
		defer cancel()
		err := c.SetDrive(ctx, name, server, ws)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			q.finish(a)
		})
	})
}

// finish opens the new session screen with the default model, every
// connected worker, and the first message typed, for the user to send.
func (q *quickstart) finish(a *App) {
	cat := q.s.list.cat
	if cat == nil {
		a.SayError(errors.New("the catalog is not loaded"))
		return
	}
	cf, err := defaultConf(cat, q.providers, nil)
	if err != nil {
		a.SayError(err)
		return
	}
	for _, w := range q.workers {
		if w.Refused == "" {
			cf.Workers = append(cf.Workers, statefile.Worker{Name: w.Name})
		}
	}
	q.cancel()
	a.Ticking(false)
	s := q.s
	s.cfg = &cf
	s.input.Clear()
	s.input.insert(textElems(qsPrompt)...)
	a.SetScreen(s)
	a.Focus(s.box.Kids[1])
}

// Key: on the splash, any key starts. Then Up, Down, Tab and the arrows move
// over the rows and buttons, Enter or Space presses, a row's number copies
// or changes it, and Esc goes back.
func (q *quickstart) Key(a *App, k term.Key) bool {
	if q.step == stepSplash {
		q.to(stepProviders)
		return true
	}
	n := len(q.rows) + len(q.buttons)
	switch s := k.String(); s {
	case "up", "left", "shift+tab":
		q.move(-1, n)
	case "down", "right", "tab":
		q.move(1, n)
	case "enter", "space":
		q.press(a, q.at())
	case "esc":
		q.to(q.step - 1)
	default:
		if i := strings.Index("123456789", s); len(s) == 1 && i >= 0 && i < len(q.rows) {
			q.sel = i
			q.press(a, i)
		}
	}
	return true
}

// move steps the selection by d, past buttons that are off.
func (q *quickstart) move(d, n int) {
	for i := q.at() + d; i >= 0 && i < n; i += d {
		if i < len(q.rows) || q.buttons[i-len(q.rows)].on {
			q.sel = i
			return
		}
	}
}

// press does what row or button i does.
func (q *quickstart) press(a *App, i int) {
	switch {
	case i < 0:
	case i < len(q.rows):
		q.rows[i].do(a)
	case i < len(q.rows)+len(q.buttons) && q.buttons[i-len(q.rows)].on:
		q.buttons[i-len(q.rows)].do(a)
	}
}

// qsRows draws the step's rows: a number and a label, then the text,
// wrapped, under it, and a blank row after each.
type qsRows struct {
	q   *quickstart
	ats []int // the first screen row of each row, at the last draw
}

func (r *qsRows) text(i int) *Text { return &Text{S: r.q.rows[i].text} }

func (r *qsRows) Height(w int) int {
	h := 0
	for i := range r.q.rows {
		h += 2 + r.text(i).Height(w-2)
	}
	return h
}

func (r *qsRows) Draw(a *App, c *Canvas, rc Rect) {
	r.ats = r.ats[:0]
	y := rc.Y
	for i, row := range r.q.rows {
		r.ats = append(r.ats, y)
		t := r.text(i)
		th := t.Height(rc.W - 2)
		label, st := a.Theme.muted(), a.Theme.text()
		if r.q.at() == i {
			c.Fill(Rect{rc.X, y, rc.W, 1 + th}, a.Theme.Panel)
			label = term.Style{FG: a.Theme.Accent, Attr: term.Bold}
		}
		c.Text(rc.X, y, fmt.Sprintf("%d  %s", i+1, row.label), label)
		t.Style = st
		t.Draw(a, c, Rect{rc.X + 2, y + 1, rc.W - 2, th})
		y += 2 + th
	}
}

func (r *qsRows) Mouse(a *App, m term.Mouse, rc Rect) bool {
	if m.Action != term.Press || m.Button != term.Left {
		return false
	}
	for i := len(r.ats) - 1; i >= 0; i-- {
		if m.Y >= r.ats[i] {
			r.q.sel = i
			r.q.press(a, i)
			return true
		}
	}
	return false
}

// qsBar draws the buttons in a row, right aligned.
type qsBar struct {
	q  *quickstart
	xs [][2]int // each button's columns, at the last draw
}

func (b *qsBar) Draw(a *App, c *Canvas, r Rect) {
	q := b.q
	w := 0
	for _, bt := range q.buttons {
		w += term.Width(bt.label) + 6
	}
	x := r.X + max(0, r.W-w+2)
	b.xs = b.xs[:0]
	for i, bt := range q.buttons {
		bw := term.Width(bt.label) + 4
		st := term.Style{FG: a.Theme.Text, BG: a.Theme.Panel}
		switch {
		case !bt.on:
			st = term.Style{FG: a.Theme.Muted, BG: a.Theme.Panel}
		case q.at() == len(q.rows)+i:
			st = term.Style{FG: a.Theme.BG, BG: a.Theme.Accent, Attr: term.Bold}
		}
		c.Fill(Rect{x, r.Y, bw, 1}, st.BG)
		c.Text(x+2, r.Y, bt.label, st)
		b.xs = append(b.xs, [2]int{x, x + bw})
		x += bw + 2
	}
}

func (b *qsBar) Mouse(a *App, m term.Mouse, _ Rect) bool {
	if m.Action != term.Press || m.Button != term.Left {
		return false
	}
	for i, x := range b.xs {
		if m.X >= x[0] && m.X < x[1] {
			b.q.press(a, len(b.q.rows)+i)
			return true
		}
	}
	return false
}
