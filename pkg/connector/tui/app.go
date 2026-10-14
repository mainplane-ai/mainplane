package tui

import (
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

const (
	toastTime = 2 * time.Second
	// dim is how far a modal layer darkens what is under it.
	dim = 0.4
	// tickRate is the frame rate of animation, here the tab spinner.
	tickRate = 100 * time.Millisecond
)

// Screen is the tree at the bottom of the layers, and its keys.
type Screen interface {
	Root() *Node
	// Size is called before every layout, for breakpoints and caps.
	Size(a *App, w, h int)
	Key(a *App, k term.Key) bool
	// Tick is called at tickRate while the app ticks.
	Tick(a *App)
}

// Selector is a view that holds a text selection.
type Selector interface {
	Selected() string
	ClearSelection()
}

// App runs the loop: one event, then one frame of what changed. All state
// is touched only from the loop; other goroutines hand it work with Post.
type App struct {
	T     *term.Terminal
	Theme Theme
	W, H  int

	screen     Screen
	layers     []*Layer // over the screen, bottom first
	focus      *Node
	capture    *Node // gets the mouse from a press to its release
	sel        Selector
	toastLayer *Layer
	r          *term.Renderer
	post       chan func()
	ticker     *time.Ticker
	lastKey    string
	prevKey    string
	lastAt     time.Time
	prevAt     time.Time
	quit       bool
	Update     string // the release to update to, when the loop ends for it
}

func NewApp(t *term.Terminal) *App {
	a := &App{T: t, Theme: themeFor(t.Caps.Dark), r: term.NewRenderer(t), post: make(chan func(), 64)}
	a.W, a.H = t.Size()
	return a
}

// Post runs f on the loop. It is safe from any goroutine.
func (a *App) Post(f func()) { a.post <- f }

// Go runs f on its own goroutine, restoring the terminal if it panics.
func (a *App) Go(f func()) {
	go func() {
		defer a.T.Recover()
		f()
	}()
}

func (a *App) SetScreen(s Screen) {
	a.screen, a.capture, a.sel = s, nil, nil
}

func (a *App) Focus(n *Node) { a.focus = n }

// Select makes s the holder of the selection Ctrl+C copies, clearing the one
// before.
func (a *App) Select(s Selector) {
	if a.sel != nil && a.sel != s {
		a.sel.ClearSelection()
	}
	a.sel = s
}

// Ticking turns the animation tick on or off.
func (a *App) Ticking(on bool) {
	switch {
	case on && a.ticker == nil:
		a.ticker = time.NewTicker(tickRate)
	case !on && a.ticker != nil:
		a.ticker.Stop()
		a.ticker = nil
	}
}

func (a *App) Quit() { a.quit = true }

// Run loops until Quit. Events that arrive together make one frame.
func (a *App) Run() {
	for !a.quit {
		a.draw()
		var tick <-chan time.Time
		if a.ticker != nil {
			tick = a.ticker.C
		}
		select {
		case e := <-a.T.Events:
			a.event(e)
		case f := <-a.post:
			f()
		case <-tick:
			a.screen.Tick(a)
		}
		for more := true; more && !a.quit; {
			select {
			case e := <-a.T.Events:
				a.event(e)
			case f := <-a.post:
				f()
			default:
				more = false
			}
		}
	}
}

func (a *App) draw() {
	a.screen.Size(a, a.W, a.H)
	b := term.NewBuffer(a.W, a.H)
	c := &Canvas{b, Rect{0, 0, a.W, a.H}}
	root := a.screen.Root()
	root.Layout(Rect{0, 0, a.W, a.H})
	root.Draw(a, c)
	for _, l := range a.layers {
		if l.Modal {
			for i := range b.Cells {
				b.Cells[i].FG = b.Cells[i].FG.Blend(term.RGB(0, 0, 0), dim)
				b.Cells[i].BG = b.Cells[i].BG.Blend(term.RGB(0, 0, 0), dim)
			}
			b.Cursor = false
		}
		l.Root.Layout(l.Place(a.W, a.H))
		l.Root.Draw(a, c)
	}
	a.r.Render(b)
}

func (a *App) event(e term.Event) {
	switch e := e.(type) {
	case term.Resize:
		a.W, a.H = e.W, e.H
		a.r.Invalidate()
	case term.Key:
		a.key(e)
	case term.Mouse:
		a.mouse(e)
	case term.Paste:
		if p, ok := a.focusView().(PasteHandler); ok {
			p.Paste(a, e.Text)
		}
	case term.Focus:
	}
}

func (a *App) focusView() View {
	if a.focus == nil {
		return nil
	}
	return a.focus.View
}

// key goes to the top modal layer, else the focused node and its parents,
// then the screen. Ctrl+C is the app's: with a selection it copies, without
// one it exits, and twice in a row it always exits.
func (a *App) key(k term.Key) {
	s := k.String()
	a.prevKey, a.lastKey = a.lastKey, s
	a.prevAt, a.lastAt = a.lastAt, time.Now()
	if s == "ctrl+c" {
		if !a.Again(0) && a.sel != nil && a.sel.Selected() != "" {
			a.Copy(a.sel.Selected())
			a.sel.ClearSelection()
			return
		}
		a.Quit()
		return
	}
	if l := a.top(); l != nil {
		dispatchKey(a, l.Root, k)
		return
	}
	for n := a.focus; n != nil; n = n.parent {
		if h, ok := n.View.(KeyHandler); ok && h.Key(a, k) {
			return
		}
	}
	a.screen.Key(a, k)
}

// Again is whether the key being handled is the same as the one before it,
// pressed within d; 0 is any time.
func (a *App) Again(d time.Duration) bool {
	return a.prevKey == a.lastKey && (d == 0 || a.lastAt.Sub(a.prevAt) < d)
}

func dispatchKey(a *App, n *Node, k term.Key) bool {
	if h, ok := n.View.(KeyHandler); ok && h.Key(a, k) {
		return true
	}
	for _, c := range n.Kids {
		if dispatchKey(a, c, k) {
			return true
		}
	}
	return false
}

func (a *App) top() *Layer {
	for i := len(a.layers) - 1; i >= 0; i-- {
		if a.layers[i].Modal {
			return a.layers[i]
		}
	}
	return nil
}

// mouse goes to the node holding the capture, else to the node under it on
// the top layer that takes it, and up its parents until one handles it.
func (a *App) mouse(m term.Mouse) {
	n := a.capture
	if n == nil {
		n = a.hit(m.X, m.Y)
	}
	if m.Action == term.Release {
		a.capture = nil
	}
	for ; n != nil; n = n.parent {
		if h, ok := n.View.(MouseHandler); ok && h.Mouse(a, m, n.content()) {
			if m.Action == term.Press && m.Button == term.Left {
				a.capture = n
			}
			return
		}
	}
}

func (a *App) hit(x, y int) *Node {
	for i := len(a.layers) - 1; i >= 0; i-- {
		l := a.layers[i]
		if h := l.Root.Hit(x, y); h != nil && (l.Modal || takesMouse(h)) {
			return h
		}
		if l.Modal {
			return nil
		}
	}
	return a.screen.Root().Hit(x, y)
}

// takesMouse is whether n or a parent has a view that handles the mouse. A
// layer that has none, like a toast, lets clicks through.
func takesMouse(n *Node) bool {
	for ; n != nil; n = n.parent {
		if _, ok := n.View.(MouseHandler); ok {
			return true
		}
	}
	return false
}

// Copy puts text on the clipboard and says so.
func (a *App) Copy(text string) {
	a.T.Copy(text)
	a.Toast("Copied")
}

func (a *App) Push(l *Layer) { a.layers = append(a.layers, l) }

func (a *App) Pop(l *Layer) {
	for i, x := range a.layers {
		if x == l {
			a.layers = append(a.layers[:i], a.layers[i+1:]...)
			return
		}
	}
}
