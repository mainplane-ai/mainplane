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

// The quickstart's words, all here, to be rewritten together. A step's words
// are markdown, drawn as the transcript draws the agent's text.
const (
	qsAnyKey      = "Press any key to get started"
	qsProviders   = "Connect a model provider to begin"
	qsWorkers     = "## OPTIONAL: Connect workers\n\nA worker is a machine your agents use. Run this one-line install on a machine to connect it."
	qsConnectors  = "## OPTIONAL: Message Mainplane on another device\n\nYour sessions sync across any device with a Mainplane API key. Run this one-line install to add the TUI to another device, then run `mainplane` there."
	qsDrives      = "## OPTIONAL: Host a shared drive\n\nMainplane can host files shared among all your workers. Add a shared drive for all workers?"
	qsConnected   = "Connected: "
	qsNetStarting = "The network is starting. The lines work once it is ready."
	qsNetFailed   = "The network failed: "
	qsMinting     = "Making an api key..."
	qsKeyName     = "tui-%d" // the api key a quickstart mints, the first number free
	qsDriveName   = "shared"
	qsCommand     = "/providers"
	qsPrompt      = "Tell me what devices you have access to and what you can do"
)

// The quickstart's steps, in order.
const (
	stepSplash = iota
	stepProviders
	stepWorkers
	stepConnectors
	stepDrives
)

// quickstart is the first run on the harness's own machine: a provider,
// then workers, a connector and a drive, each optional, then the first
// session. Each step is the wordmark, its words, and the keys under them:
// Enter continues, Esc goes back. It reads what it shows from the harness
// every pollEvery and stores nothing: the harness's state is the progress.
type quickstart struct {
	s      *session // the screen it ends on; its commands connect providers
	ctx    context.Context
	cancel context.CancelFunc
	step   int
	polled bool // the providers were read once: a new one after that moves on

	providers []harness.ProviderInfo
	workers   []connector.Worker
	network   string
	join      string
	key       string // the api key minted for a connector
	minting   bool

	root, splash, steps, col *Node
	plane                    *plane
	words                    string // the step's words, which body shows
	body                     *Node  // the words in a Scrollbox, to select and copy as in the transcript
	input                    *Input // the providers step's, which takes only /providers
	slash                    *slash // its list of commands, which is /providers alone
	slashRow                 *Node
	inputRow                 *Node
	hint                     *Node
	left, right              *Glyphs // the keys, as the status line under the input
}

func newQuickstart(a *App, c connector.Client) *quickstart {
	q := &quickstart{s: newSession(a, newSessionList(a, c), ""), input: NewInput(), body: &Node{}, left: &Glyphs{}, right: &Glyphs{}}
	q.ctx, q.cancel = context.WithCancel(context.Background())
	q.input.Placeholder, q.input.Before = qsCommand, q.connect
	cmd := commands()[slices.IndexFunc(commands(), func(c command) bool { return "/"+c.name == qsCommand })]
	run := cmd.run
	cmd.run = func(s *session, a *App) { // typed again after, for Enter to open the providers again until one is connected
		run(s, a)
		if len(q.input.els) == 0 && len(q.providers) == 0 {
			q.input.insert(textElems(qsCommand)...)
		}
	}
	q.slash = &slash{s: q.s, in: q.input, all: []command{cmd}}
	q.input.insert(textElems(qsCommand)...)
	q.splash = Box(&Node{Dir: Col, Grow: 1}, &Node{Grow: 1},
		&Node{View: Wordmark{}}, &Node{Fixed: 1},
		&Node{View: &Text{S: qsAnyKey, Center: true, Style: a.Theme.text()}},
		&Node{Grow: 1})
	q.plane = &plane{on: func() bool { return q.step == stepSplash }}
	q.slashRow = Box(&Node{Dir: Row}, &Node{Fixed: 1, View: bar{a.Theme.Muted}}, &Node{Grow: 1, BG: a.Theme.Panel, View: q.slash})
	q.inputRow = Box(&Node{Dir: Col, Pad: [4]int{0, 1, 0, 0}}, inputBox(a, q.input))
	q.hint = Box(&Node{Dir: Row, Pad: [4]int{margin, 0, 0, 0}}, &Node{Grow: 1, View: q.left}, &Node{View: q.right})
	q.col = Box(&Node{Dir: Col}, &Node{View: Wordmark{}}, &Node{Fixed: 1}, q.body, q.slashRow, q.inputRow, q.hint)
	q.steps = Box(&Node{Dir: Col, Grow: 1}, &Node{Grow: 1}, centered(q.col), &Node{Grow: 1})
	q.root = Box(&Node{Dir: Col, BG: a.Theme.BG, Over: q.plane}, q.splash, q.steps)
	a.Focus(nil)
	q.poll(a)
	return q
}

// poll reads the providers, workers, network and join lines until the
// quickstart ends. A provider connected on the providers step moves on.
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
				more := q.polled && len(ps) > len(q.providers)
				q.providers, q.workers, q.network, q.join, q.polled = ps, ws, network, join, true
				if more && q.step == stepProviders {
					q.next(a)
				}
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
	q.splash.Hidden, q.steps.Hidden = q.step != stepSplash, q.step == stepSplash
	q.col.Fixed = min(qsWidth, w-4)
	q.layout(a)
	q.body.Hidden = true
	rest := h - q.steps.measure(w)
	q.body.Hidden = false
	q.body.Fixed = max(1, min(len((&textBlock{q.words}).Rows(a, q.col.Fixed)), rest))
	a.Frame = 0
	splash := q.step == stepSplash && !a.Blurred
	if splash {
		a.Frame = planeFrame
	}
	a.Ticking(splash)
}

// Tick opens the providers once the plane has flown off.
func (q *quickstart) Tick(a *App) {
	if q.step == stepSplash && q.plane.flown() {
		q.to(a, stepProviders)
	}
}

// layout is the step's words and keys, from what the harness said last. An
// error takes the place of the left key, as the model's in the status line.
func (q *quickstart) layout(a *App) {
	back, next := "to go back", "to continue"
	switch q.step {
	case stepProviders:
		q.show(qsProviders + q.providerNote())
	case stepWorkers:
		q.show(qsWorkers + q.lines(q.join) + q.workerNote())
	case stepConnectors:
		words := qsConnectors + q.lines(q.key)
		if q.minting {
			words += "\n\n" + qsMinting
		}
		q.show(words)
	case stepDrives:
		q.show(qsDrives)
		back, next = "to skip", "to create"
	}
	q.inputRow.Hidden = q.step != stepProviders
	q.slash.update()
	q.slashRow.Hidden = q.inputRow.Hidden || len(q.slash.cmds) == 0
	q.inputRow.Pad[1] = 1 // the blank row above the input, which the list takes
	if !q.slashRow.Hidden {
		q.inputRow.Pad[1] = 0
	}
	key, muted := term.Style{FG: a.Theme.Text, Attr: term.Bold}, a.Theme.muted()
	q.left.Gs = cat(run("esc", key), run(" "+back, muted))
	if a.Status.S != "" {
		q.left.Gs = run(a.Status.S, a.Status.Style)
	}
	q.right.Gs = cat(run("enter", key), run(" "+next, muted))
	q.hint.Kids[1].Fixed = gwidth(q.right.Gs)
}

// show puts words in the body, in a new Scrollbox when they change.
func (q *quickstart) show(words string) {
	if words == q.words {
		return
	}
	q.words = words
	sb, b := NewScrollbox(), &textBlock{words}
	sb.Append(b)
	sb.Show(b) // from the top when it does not fit
	q.body.View = sb
}

// providerNote names the providers connected, by the catalog's names.
func (q *quickstart) providerNote() string {
	if len(q.providers) == 0 {
		return ""
	}
	var names []string
	for _, p := range q.providers {
		name := p.Name
		if cat := q.s.list.cat; cat != nil && cat.Providers[name].Name != "" {
			name = cat.Providers[name].Name
		}
		names = append(names, name)
	}
	return "\n\n" + qsConnected + strings.Join(names, ", ")
}

// workerNote is the network while it is not ready and the workers connected.
func (q *quickstart) workerNote() string {
	var b strings.Builder
	switch q.network {
	case "ready":
	case "starting":
		b.WriteString("\n\n" + qsNetStarting)
	default:
		b.WriteString("\n\n" + qsNetFailed + q.network)
	}
	var names []string
	for _, w := range q.workers {
		if w.Refused != "" {
			names = append(names, w.Name+" (refused: "+w.Refused+")")
			continue
		}
		names = append(names, w.Name)
	}
	if len(names) > 0 {
		b.WriteString("\n\n" + qsConnected + strings.Join(names, ", "))
	}
	return b.String()
}

// lines are the install script run with args, for Linux and macOS and for
// Windows, in a code block; none until args are known.
func (q *quickstart) lines(args string) string {
	if args == "" {
		return ""
	}
	unix, windows := release.InstallLines(args)
	return "\n\n```\n# linux, macos\n" + unix + "\n\n# windows\n" + windows + "\n```"
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

// to opens step n. The connectors step mints its key on the way in, so a
// failed mint is tried again only when the step is opened again.
func (q *quickstart) to(a *App, n int) {
	q.step = n
	a.Focus(nil)
	switch {
	case n == stepSplash:
		q.plane.flying = false
	case n == stepProviders:
		a.Focus(q.inputRow.Kids[0].Kids[1])
		if len(q.providers) > 0 { // back here: Enter continues, and /providers, typed, connects another
			q.input.Clear()
		}
	case n == stepConnectors && q.key == "" && !q.minting:
		q.mint(a)
	}
}

// next opens the step after this one; the drives step only when a worker
// can serve one. After the last, the session. Leaving the providers step
// asks for data sharing permission.
func (q *quickstart) next(a *App) {
	if q.step == stepProviders {
		askSharing(a, q.s.c)
	}
	if q.step == stepDrives || q.step == stepConnectors && len(q.servers()) == 0 {
		q.finish(a)
		return
	}
	q.to(a, q.step+1)
}

// connect is the input's Before: the list of commands takes its keys, as
// in a session, but Esc goes back. Enter on anything else continues once a
// provider is connected. The input does nothing else but take what is typed.
func (q *quickstart) connect(a *App, k term.Key) bool {
	switch {
	case k.String() == "esc":
		return false
	case q.slash.Key(a, k):
		return true
	case k.String() != "enter":
		return false
	case len(q.providers) > 0:
		q.next(a)
	}
	return true
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
			if q.ctx.Err() != nil { // the quickstart is over
				return
			}
			if err != nil {
				a.SayError(err)
				return
			}
			q.key = key
		})
	})
}

// saveDrive makes the shared drive for every worker, served by admin when it
// can serve one, else by the first worker that can. Then the session.
func (q *quickstart) saveDrive(a *App) {
	srv := q.servers()
	if len(srv) == 0 {
		a.SayError(errors.New("no connected worker can serve a drive"))
		return
	}
	server := srv[0]
	if slices.Contains(srv, "admin") {
		server = "admin"
	}
	c := q.s.c
	a.Go(func() {
		ctx, cancel := context.WithTimeout(q.ctx, callTime)
		defer cancel()
		err := c.SetDrive(ctx, qsDriveName, server, []string{"*"})
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

// Key: on the splash, any key flies the plane off, and a key in its flight
// opens the providers at once. Then Enter continues and Esc goes back; on
// the drives step Enter makes the drive and Esc skips it.
func (q *quickstart) Key(a *App, k term.Key) bool {
	if q.step == stepSplash {
		if !q.plane.flying {
			q.plane.flying, q.plane.fly = true, q.plane.t
		} else {
			q.to(a, stepProviders)
		}
		return true
	}
	switch k.String() {
	case "enter":
		if q.step == stepDrives {
			q.saveDrive(a)
		} else {
			q.next(a)
		}
	case "esc":
		if q.step == stepDrives {
			q.finish(a)
		} else {
			q.to(a, q.step-1)
		}
	}
	return true
}
