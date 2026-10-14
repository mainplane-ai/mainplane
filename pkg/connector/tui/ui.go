package tui

import "github.com/mainplane-ai/mainplane/pkg/connector/tui/term"

type Rect struct{ X, Y, W, H int }

func (r Rect) Has(x, y int) bool { return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H }

// Inset is r less l, t, rt and b cells on each side.
func (r Rect) Inset(l, t, rt, b int) Rect {
	return Rect{r.X + l, r.Y + t, max(0, r.W-l-rt), max(0, r.H-t-b)}
}

func (r Rect) intersect(o Rect) Rect {
	x, y := max(r.X, o.X), max(r.Y, o.Y)
	return Rect{x, y, max(0, min(r.X+r.W, o.X+o.W)-x), max(0, min(r.Y+r.H, o.Y+o.H)-y)}
}

// Canvas draws on a buffer, clipped to a rect.
type Canvas struct {
	b    *term.Buffer
	clip Rect
}

func (c *Canvas) Sub(r Rect) *Canvas { return &Canvas{c.b, c.clip.intersect(r)} }

// Fill paints r with bg and erases what was there.
func (c *Canvas) Fill(r Rect, bg term.Color) {
	r = r.intersect(c.clip)
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c.b.Set(x, y, " ", 1, term.Style{BG: bg})
		}
	}
}

// Set puts one grapheme. A style with no background keeps the cell's.
func (c *Canvas) Set(x, y int, g string, w int, s term.Style) {
	if !c.clip.Has(x, y) {
		return
	}
	if w == 2 && !c.clip.Has(x+1, y) {
		g, w = " ", 1
	}
	if s.BG == 0 {
		if old := c.b.At(x, y); old != nil {
			s.BG = old.BG
		}
	}
	c.b.Set(x, y, g, w, s)
}

// Text draws one line from x and returns the x after it.
func (c *Canvas) Text(x, y int, s string, st term.Style) int {
	term.Graphemes(s, func(g string, w int) {
		c.Set(x, y, g, w, st)
		x += w
	})
	return x
}

// Restyle changes the style of the cell at x, y.
func (c *Canvas) Restyle(x, y int, f func(*term.Style)) {
	if !c.clip.Has(x, y) {
		return
	}
	if cell := c.b.At(x, y); cell != nil {
		f(&cell.Style)
	}
}

func (c *Canvas) Cursor(x, y int) {
	if c.clip.Has(x, y) {
		c.b.Cursor, c.b.CursorX, c.b.CursorY = true, x, y
	}
}

type Dir uint8

const (
	Col Dir = iota
	Row
)

// View is what a node shows in its content rect. A view may also be a
// Measurer, KeyHandler, MouseHandler or PasteHandler.
type View interface {
	Draw(a *App, c *Canvas, r Rect)
}

// Measurer is a view whose height follows its content at a width.
type Measurer interface{ Height(w int) int }

type KeyHandler interface{ Key(a *App, k term.Key) bool }

type MouseHandler interface {
	Mouse(a *App, m term.Mouse, r Rect) bool
}

type PasteHandler interface{ Paste(a *App, s string) }

// Node is one element of the retained tree: a box that lays its children out
// in a row or a column. On its parent's axis it is Fixed cells, or a share of
// what is left by Grow, or, with neither, the height it measures; then
// clamped to Min and Max.
type Node struct {
	Dir      Dir
	Fixed    int
	Grow     int
	Min, Max int
	Pad      [4]int // left, top, right, bottom
	BG       term.Color
	Hidden   bool
	Kids     []*Node
	View     View
	Rect     Rect // where layout put it
	parent   *Node
}

// Box is a node with children, which it adopts.
func Box(n *Node, kids ...*Node) *Node {
	n.Kids = kids
	for _, k := range kids {
		k.parent = n
	}
	return n
}

func (n *Node) content() Rect { return n.Rect.Inset(n.Pad[0], n.Pad[1], n.Pad[2], n.Pad[3]) }

func (n *Node) Layout(r Rect) {
	n.Rect = r
	in := n.content()
	main, cross := in.H, in.W
	if n.Dir == Row {
		main, cross = in.W, in.H
	}
	sizes := make([]int, len(n.Kids))
	used, grow := 0, 0
	for i, k := range n.Kids {
		switch {
		case k.Hidden:
			continue
		case k.Fixed > 0:
			sizes[i] = k.clamp(k.Fixed)
		case k.Grow > 0:
			grow += k.Grow
			continue
		case n.Dir == Col:
			sizes[i] = k.clamp(k.measure(cross))
		}
		used += sizes[i]
	}
	rest := max(0, main-used)
	for i, k := range n.Kids {
		if !k.Hidden && k.Fixed == 0 && k.Grow > 0 {
			sizes[i] = k.clamp(rest * k.Grow / grow)
			rest -= sizes[i]
			grow -= k.Grow
		}
	}
	at := 0
	for i, k := range n.Kids {
		if k.Hidden {
			continue
		}
		s := min(sizes[i], max(0, main-at))
		if n.Dir == Col {
			k.Layout(Rect{in.X, in.Y + at, in.W, s})
		} else {
			k.Layout(Rect{in.X + at, in.Y, s, in.H})
		}
		at += s
	}
}

// measure is the height n needs at width w: its view's and its children's,
// stacked in a column or side by side in a row, where a Grow child gets its
// share of the width the Fixed ones leave, as Layout gives it.
func (n *Node) measure(w int) int {
	in, h, fixed, grow := w-n.Pad[0]-n.Pad[2], 0, 0, 0
	if m, ok := n.View.(Measurer); ok {
		h = m.Height(in)
	}
	for _, k := range n.Kids {
		switch {
		case k.Hidden || n.Dir != Row:
		case k.Fixed > 0:
			fixed += k.clamp(k.Fixed)
		case k.Grow > 0:
			grow += k.Grow
		}
	}
	for _, k := range n.Kids {
		switch {
		case k.Hidden:
		case n.Dir == Col && k.Fixed > 0:
			h += k.clamp(k.Fixed)
		case n.Dir == Col:
			h += k.clamp(k.measure(in))
		case k.Fixed > 0:
			h = max(h, k.measure(k.clamp(k.Fixed)))
		case k.Grow > 0:
			h = max(h, k.measure(k.clamp(max(0, in-fixed)*k.Grow/grow)))
		}
	}
	return h + n.Pad[1] + n.Pad[3]
}

func (n *Node) clamp(s int) int {
	if n.Max > 0 {
		s = min(s, n.Max)
	}
	return max(s, n.Min)
}

func (n *Node) Draw(a *App, c *Canvas) {
	if n.Hidden || n.Rect.W == 0 || n.Rect.H == 0 {
		return
	}
	c = c.Sub(n.Rect)
	if n.BG != 0 {
		c.Fill(n.Rect, n.BG)
	}
	if n.View != nil {
		in := n.content()
		n.View.Draw(a, c.Sub(in), in)
	}
	for _, k := range n.Kids {
		k.Draw(a, c)
	}
}

// Hit is the deepest visible node at x, y.
func (n *Node) Hit(x, y int) *Node {
	if n.Hidden || !n.Rect.Has(x, y) {
		return nil
	}
	for i := len(n.Kids) - 1; i >= 0; i-- {
		if h := n.Kids[i].Hit(x, y); h != nil {
			return h
		}
	}
	return n
}

// Layer is a tree drawn over the ones below it: the screen, a dialog, a
// toast. Place gives its rect for a screen size. A modal layer takes every
// key and click and dims what is under it.
type Layer struct {
	Root  *Node
	Modal bool
	Place func(w, h int) Rect
}
