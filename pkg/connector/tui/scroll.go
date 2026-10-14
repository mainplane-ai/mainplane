package tui

import (
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

// wheelRows is how far one wheel notch scrolls.
const wheelRows = 3

// Scrollbox is a column of wrapped text that scrolls, with a scrollbar and
// mouse selection. It sticks to the bottom until scrolled up.
type Scrollbox struct {
	Bar   bool // show the scrollbar: 1 column and 1 padding on the right
	Style term.Style

	lines  []string
	rows   []srow
	width  int // rows are wrapped for this width; 0 is not yet
	top    int
	sticky bool
	view   Rect // the text, as last drawn
	barX   int
	onBar  bool
	sel    struct {
		on   bool
		a, b pos
	}
}

type srow struct {
	line int
	gs   []glyph
	w    int
}

type pos struct{ row, col int }

func NewScrollbox() *Scrollbox { return &Scrollbox{sticky: true} }

// Append adds text as lines; a final line break ends the last line.
func (s *Scrollbox) Append(text string) {
	from := len(s.lines)
	s.lines = append(s.lines, strings.Split(strings.TrimSuffix(text, "\n"), "\n")...)
	if s.width > 0 {
		s.wrapFrom(from)
	}
}

func (s *Scrollbox) Empty() bool { return len(s.lines) == 0 }

func (s *Scrollbox) wrapFrom(from int) {
	for i, l := range s.lines[from:] {
		gs := glyphs(l)
		for _, r := range wrapLine(gs, s.width) {
			w := 0
			for _, g := range gs[r[0]:r[1]] {
				w += g.w
			}
			s.rows = append(s.rows, srow{from + i, gs[r[0]:r[1]], w})
		}
	}
}

func (s *Scrollbox) maxTop() int { return max(0, len(s.rows)-s.view.H) }

// Scroll moves the view by n rows; at the bottom it sticks again.
func (s *Scrollbox) Scroll(n int) {
	s.top = min(max(0, s.top+n), s.maxTop())
	s.sticky = s.top == s.maxTop()
}

func (s *Scrollbox) Page() int { return max(1, s.view.H-1) }

func (s *Scrollbox) Draw(a *App, c *Canvas, r Rect) {
	tw := r.W
	if s.Bar && r.W > 2 {
		tw = r.W - 2
	}
	if tw != s.width {
		s.width, s.rows, s.sel.on = max(1, tw), nil, false
		s.wrapFrom(0)
	}
	s.view, s.barX = Rect{r.X, r.Y, tw, r.H}, r.X+r.W-1
	if s.sticky {
		s.top = s.maxTop()
	}
	s.top = min(s.top, s.maxTop())
	a1, b1 := s.order()
	for y := 0; y < r.H && s.top+y < len(s.rows); y++ {
		ri := s.top + y
		x := r.X
		for _, g := range s.rows[ri].gs {
			st := s.Style
			if s.sel.on && s.picked(ri, x-r.X, g.w, a1, b1) {
				st.BG = a.Theme.Selection
			}
			c.Set(x, r.Y+y, g.g, g.w, st)
			x += g.w
		}
	}
	if s.Bar && r.W > 2 && len(s.rows) > r.H {
		thumb := max(1, r.H*r.H/len(s.rows))
		at := s.top * (r.H - thumb) / s.maxTop()
		for y := range r.H {
			bg := a.Theme.Element
			if y >= at && y < at+thumb {
				bg = a.Theme.Muted
			}
			c.Set(s.barX, r.Y+y, " ", 1, term.Style{BG: bg})
		}
	}
}

func (p pos) in(a, b pos) bool {
	return (p.row > a.row || p.row == a.row && p.col >= a.col) && (p.row < b.row || p.row == b.row && p.col <= b.col)
}

// picked is whether the grapheme at row ri, column x, w wide, has a cell in
// the selection from a to b.
func (s *Scrollbox) picked(ri, x, w int, a, b pos) bool {
	return (pos{ri, x}).in(a, b) || w == 2 && (pos{ri, x + 1}).in(a, b)
}

func (s *Scrollbox) order() (pos, pos) {
	a, b := s.sel.a, s.sel.b
	if b.row < a.row || b.row == a.row && b.col < a.col {
		a, b = b, a
	}
	return a, b
}

func (s *Scrollbox) posAt(x, y int) pos {
	row := min(max(0, s.top+y-s.view.Y), max(0, len(s.rows)-1))
	return pos{row, min(max(0, x-s.view.X), s.view.W-1)}
}

func (s *Scrollbox) Mouse(a *App, m term.Mouse, r Rect) bool {
	switch {
	case m.Button == term.WheelUp:
		s.Scroll(-wheelRows)
	case m.Button == term.WheelDown:
		s.Scroll(wheelRows)
	case m.Action == term.Press && m.Button == term.Left:
		s.onBar = s.Bar && m.X == s.barX && len(s.rows) > s.view.H
		if s.onBar {
			s.barTo(m.Y)
			break
		}
		a.Select(s)
		s.sel.on, s.sel.a = false, s.posAt(m.X, m.Y)
		s.sel.b = s.sel.a
	case m.Action == term.Motion && m.Button == term.Left && s.onBar:
		s.barTo(m.Y)
	case m.Action == term.Motion && m.Button == term.Left:
		if m.Y < s.view.Y {
			s.Scroll(-1)
		} else if m.Y >= s.view.Y+s.view.H {
			s.Scroll(1)
		}
		s.sel.b = s.posAt(m.X, min(max(m.Y, s.view.Y), s.view.Y+s.view.H-1))
		s.sel.on = s.sel.a != s.sel.b
		if s.sel.on {
			a.Select(s)
		}
	case m.Action == term.Release:
		if !s.onBar && s.sel.on {
			a.Copy(s.Selected())
		}
		s.onBar = false
	default:
		return false
	}
	return true
}

func (s *Scrollbox) barTo(y int) {
	s.Scroll((y-s.view.Y)*s.maxTop()/max(1, s.view.H-1) - s.top)
}

// Selected is the selected text. A row break inside a line is not a line
// break.
func (s *Scrollbox) Selected() string {
	if !s.sel.on {
		return ""
	}
	a, b := s.order()
	var out strings.Builder
	for ri := a.row; ri <= b.row && ri < len(s.rows); ri++ {
		x := 0
		for _, g := range s.rows[ri].gs {
			if s.picked(ri, x, g.w, a, b) {
				out.WriteString(g.g)
			}
			x += g.w
		}
		if ri < b.row && ri+1 < len(s.rows) && s.rows[ri+1].line != s.rows[ri].line {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func (s *Scrollbox) ClearSelection() { s.sel.on = false }
