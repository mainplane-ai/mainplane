package tui

import "github.com/mainplane-ai/mainplane/pkg/connector/tui/term"

// dialogWidth is the widest a dialog gets.
const dialogWidth = 60

// List is a column of items, one selected. Enter or a click picks it.
type List struct {
	Items  []string
	Sel    int
	OnPick func(a *App, i int)

	top int
	h   int
}

func (l *List) Height(int) int { return len(l.Items) }

func (l *List) Draw(a *App, c *Canvas, r Rect) {
	l.h = r.H
	l.top = min(max(l.top, l.Sel-r.H+1), l.Sel)
	for y := 0; y < r.H && l.top+y < len(l.Items); y++ {
		st := a.Theme.text()
		if l.top+y == l.Sel {
			st = term.Style{FG: a.Theme.BG, BG: a.Theme.Accent}
			c.Fill(Rect{r.X, r.Y + y, r.W, 1}, a.Theme.Accent)
		}
		c.Text(r.X+1, r.Y+y, l.Items[l.top+y], st)
	}
}

func (l *List) Key(a *App, k term.Key) bool {
	switch k.String() {
	case "up":
		l.Sel--
	case "down":
		l.Sel++
	case "pgup":
		l.Sel -= max(1, l.h)
	case "pgdn":
		l.Sel += max(1, l.h)
	case "home":
		l.Sel = 0
	case "end":
		l.Sel = len(l.Items) - 1
	case "enter":
		if l.OnPick != nil && len(l.Items) > 0 {
			l.OnPick(a, l.Sel)
		}
	default:
		return false
	}
	l.Sel = max(0, min(l.Sel, len(l.Items)-1))
	return true
}

func (l *List) Mouse(a *App, m term.Mouse, r Rect) bool {
	switch {
	case m.Button == term.WheelUp:
		l.Sel = max(0, l.Sel-1)
	case m.Button == term.WheelDown:
		l.Sel = min(len(l.Items)-1, l.Sel+1)
	case m.Action == term.Press && m.Button == term.Left:
		if i := l.top + m.Y - r.Y; i >= 0 && i < len(l.Items) {
			l.Sel = i
			if l.OnPick != nil {
				l.OnPick(a, i)
			}
		}
	default:
		return false
	}
	return true
}

// closer closes its layer on Esc.
type closer struct{ l *Layer }

func (closer) Draw(*App, *Canvas, Rect) {}

func (d closer) Key(a *App, k term.Key) bool {
	if k.Code == term.KeyEsc {
		a.Pop(d.l)
		return true
	}
	return false
}

// Dialog shows body under a title on a raised panel over the dimmed screen,
// until Esc. The body's view measures its height.
func (a *App) Dialog(title string, body *Node) *Layer {
	l := &Layer{Modal: true}
	head := Box(&Node{Dir: Row, Fixed: 1},
		&Node{Grow: 1, View: &Text{S: title, Style: term.Style{FG: a.Theme.Text, Attr: term.Bold}}},
		&Node{Fixed: 3, View: &Text{S: "esc", Style: a.Theme.muted()}})
	l.Root = Box(&Node{Dir: Col, BG: a.Theme.Panel, Pad: [4]int{2, 1, 2, 1}, View: closer{l}},
		head, &Node{Fixed: 1}, body)
	l.Place = func(w, h int) Rect {
		dw := min(dialogWidth, w-4)
		bh := 0
		if m, ok := body.View.(Measurer); ok {
			bh = m.Height(dw - 4)
		}
		dh := min(bh+4, h-2)
		return Rect{(w - dw) / 2, (h - dh) / 2, dw, dh}
	}
	a.Push(l)
	return l
}
