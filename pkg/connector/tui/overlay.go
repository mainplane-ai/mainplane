package tui

import "github.com/mainplane-ai/mainplane/pkg/connector/tui/term"

// dialogWidth is the widest a dialog gets.
const dialogWidth = 60

// closer closes its layer on Esc.
type closer struct{ l *Layer }

func (closer) Draw(*App, *Canvas, Rect) {}

func (d closer) Key(a *App, k term.Key) bool {
	if k.Code == term.KeyEsc {
		a.Pop(d.l)
		if d.l.Esc != nil {
			d.l.Esc()
		}
		return true
	}
	return false
}

// Dialog shows body under a title on a raised panel over the dimmed screen,
// until Esc. The body measures its height.
func (a *App) Dialog(title string, body *Node) *Layer {
	l := &Layer{Modal: true}
	head := Box(&Node{Dir: Row, Fixed: 1},
		&Node{Grow: 1, View: &Text{S: title, Style: term.Style{FG: a.Theme.Text, Attr: term.Bold}}},
		&Node{Fixed: 3, View: &Text{S: "esc", Style: a.Theme.muted()}})
	l.Root = Box(&Node{Dir: Col, BG: a.Theme.Panel, Pad: [4]int{2, 1, 2, 1}, View: closer{l}},
		head, &Node{Fixed: 1}, body)
	l.Place = func(w, h int) Rect {
		dw := min(dialogWidth, w-4)
		dh := min(body.measure(dw-4)+4, h-2)
		return Rect{(w - dw) / 2, (h - dh) / 2, dw, dh}
	}
	a.Push(l)
	return l
}
