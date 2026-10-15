package tui

import (
	"context"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// boxWidth is the widest the input is when it stands alone, on the login and
// empty session screens.
const boxWidth = 80

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// inputBox is the input on a raised shade with an accent bar on its left. The
// bar's cell keeps the screen background, as opencode's does.
func inputBox(a *App, in *Input) *Node {
	return Box(&Node{Dir: Row},
		&Node{Fixed: 1, View: &bar{}},
		&Node{Grow: 1, Pad: [4]int{1, 1, 1, 1}, BG: a.Theme.Element, View: in})
}

// bar is a vertical accent line the height of its node.
type bar struct{}

func (bar) Draw(a *App, c *Canvas, r Rect) {
	for y := range r.H {
		c.Set(r.X, r.Y+y, "┃", 1, term.Style{FG: a.Theme.Accent})
	}
}

// centered is n with its width capped, in the middle of a row.
func centered(n *Node) *Node {
	return Box(&Node{Dir: Row}, &Node{Grow: 1}, n, &Node{Grow: 1})
}

// login asks for an api key and logs in with it in the background, then
// opens the empty session screen, or ends the app to update to the harness's
// release.
type login struct {
	root   *Node
	box    *Node
	input  *Input
	status *Text // the app's status, centered
	busy   bool
	frame  int
}

func newLogin(a *App) *login {
	s := &login{input: NewInput(), status: &Text{}}
	s.input.Placeholder, s.input.Mask, s.input.OnSubmit = "mp_...", true, s.submit
	s.box = inputBox(a, s.input)
	s.root = Box(&Node{Dir: Col, BG: a.Theme.BG},
		&Node{Grow: 1},
		&Node{View: Wordmark{}},
		&Node{Fixed: 1},
		&Node{View: &Text{S: "Paste your Mainplane API key", Center: true, Style: a.Theme.muted()}},
		&Node{Fixed: 1},
		centered(s.box),
		&Node{Fixed: 1},
		&Node{View: s.status},
		&Node{Grow: 1})
	a.Focus(s.box.Kids[1])
	return s
}

func (s *login) Root() *Node { return s.root }

func (s *login) Size(a *App, w, h int) {
	s.box.Fixed = min(boxWidth, w-2)
	*s.status = *a.Status
	s.status.Center = true
}

func (s *login) Key(*App, term.Key) bool { return false }

func (s *login) Tick(a *App) {
	s.frame++
	a.Say(spinner[s.frame%len(spinner)] + " Logging in")
}

func (s *login) submit(a *App, key string) {
	if s.busy {
		return
	}
	s.busy = true
	s.Tick(a)
	a.Ticking(true)
	a.Go(func() {
		c, _, v, err := connector.Login(context.Background(), strings.TrimSpace(key))
		a.Post(func() {
			s.busy, a.Status.S = false, ""
			a.Ticking(false)
			switch {
			case err != nil:
				a.SayError(err)
			case !version.Match(v):
				a.Update = v
				a.Quit()
			default:
				a.SetScreen(newSession(a, c, ""))
			}
		})
	})
}
