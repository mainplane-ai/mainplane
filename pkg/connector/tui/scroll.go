package tui

import (
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

// wheelRows is how far one wheel notch scrolls.
const wheelRows = 3

// Block is one item of a Scrollbox: rows laid out for a width. A block that
// is also a Clicker takes clicks.
type Block interface {
	Rows(a *App, w int) []Line
}

// Clicker is a block that does something when clicked, then lays out anew.
type Clicker interface{ Click(a *App) }

// Line is one row of styled glyphs. BG fills the row from column BGFrom to the
// right edge. Cont says the row continues the line above, so a copy joins
// them without a line break.
type Line struct {
	Gs     []Glyph
	BG     term.Color
	BGFrom int
	Cont   bool
}

// Glyph is one grapheme and its style. A Deco glyph is drawn but never
// selected or copied. A Spin glyph is drawn as the spinner's frame.
type Glyph struct {
	G    string
	W    int
	St   term.Style
	Deco bool
	Spin bool
}

// Scrollbox is a column of blocks that scrolls, with a scrollbar, mouse
// selection and clicks. It sticks to the bottom until scrolled up. A blank
// row parts two blocks, unless Gap says not to.
type Scrollbox struct {
	Bar   bool // show the scrollbar: 1 column and 1 padding on the right
	Frame int  // the spinner's frame
	Gap   func(prev, next Block) bool

	items  []*item
	rows   []srow
	width  int // rows are laid out for this width; 0 is not yet
	dirty  bool
	top    int
	sticky bool
	view   Rect // the text, as last drawn
	barX   int
	onBar  bool
	onRow  bool // the press was on a row, so its release may be a click
	sel    struct {
		on   bool
		a, b pos
	}
}

type item struct {
	b     Block
	rows  []Line
	stale bool
}

// srow is a row of the whole box and the item it belongs to.
type srow struct {
	Line
	it *item
}

type pos struct{ row, col int }

func NewScrollbox() *Scrollbox { return &Scrollbox{sticky: true} }

func (s *Scrollbox) Append(b Block) {
	s.items = append(s.items, &item{b: b, stale: true})
	s.dirty = true
}

// Changed lays b out again at the next draw.
func (s *Scrollbox) Changed(b Block) {
	for _, it := range s.items {
		if it.b == b {
			it.stale, s.dirty = true, true
		}
	}
}

func (s *Scrollbox) Empty() bool { return len(s.items) == 0 }

func (s *Scrollbox) layout(a *App) {
	if !s.dirty {
		return
	}
	s.rows, s.dirty = s.rows[:0], false
	var prev Block
	from := -1 // the first row laid out anew
	for _, it := range s.items {
		if it.stale && from < 0 {
			from = len(s.rows)
		}
		if it.stale {
			it.rows, it.stale = it.b.Rows(a, s.width), false
		}
		if len(it.rows) == 0 {
			continue
		}
		if prev != nil && (s.Gap == nil || s.Gap(prev, it.b)) {
			s.rows = append(s.rows, srow{})
		}
		for _, r := range it.rows {
			s.rows = append(s.rows, srow{r, it})
		}
		prev = it.b
	}
	// rows that moved under a selection or a press would pick other text
	lo, hi := s.order()
	if from >= 0 && from <= hi.row {
		s.sel.on = false
	}
	if from >= 0 && from <= lo.row {
		s.onRow = false
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
		s.width, s.sel.on, s.dirty = max(1, tw), false, true
		for _, it := range s.items {
			it.stale = true
		}
	}
	s.layout(a)
	s.view, s.barX = Rect{r.X, r.Y, tw, r.H}, r.X+r.W-1
	if s.sticky {
		s.top = s.maxTop()
	}
	s.top = min(s.top, s.maxTop())
	a1, b1 := s.order()
	for y := 0; y < r.H && s.top+y < len(s.rows); y++ {
		ri := s.top + y
		row := s.rows[ri]
		if row.BG != 0 {
			c.Fill(Rect{r.X + row.BGFrom, r.Y + y, tw - row.BGFrom, 1}, row.BG)
		}
		x := r.X
		for _, g := range row.Gs {
			st, gs := g.St, g.G
			if g.Spin {
				gs = spinner[s.Frame%len(spinner)]
			}
			if s.sel.on && !g.Deco && s.picked(ri, x-r.X, g.W, a1, b1) {
				st.BG = a.Theme.Selection
			}
			c.Set(x, r.Y+y, gs, g.W, st)
			x += g.W
		}
	}
	if s.Bar && r.W > 2 && len(s.rows) > r.H {
		thumb := max(1, r.H*r.H/len(s.rows))
		at := s.top * (r.H - thumb) / s.maxTop()
		for y := range r.H {
			bg := a.Theme.Panel
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
		s.onRow = s.top+m.Y-s.view.Y < len(s.rows)
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
		switch {
		case s.onBar:
		case s.sel.on:
			a.Copy(s.Selected())
		case s.onRow && s.view.Has(m.X, m.Y):
			s.click(a, s.rows[s.sel.a.row].it)
		}
		s.onBar, s.onRow = false, false
	default:
		return false
	}
	return true
}

// click hands a click to its block. The row clicked stays where it was on
// the screen.
func (s *Scrollbox) click(a *App, it *item) {
	if it == nil {
		return
	}
	c, ok := it.b.(Clicker)
	if !ok {
		return
	}
	first := 0
	for first < len(s.rows) && s.rows[first].it != it {
		first++
	}
	c.Click(a)
	it.stale, s.dirty = true, true
	s.layout(a)
	if first < s.top {
		s.top = first
	}
	s.top = min(s.top, s.maxTop())
	s.sticky = s.top == s.maxTop()
}

func (s *Scrollbox) barTo(y int) {
	s.Scroll((y-s.view.Y)*s.maxTop()/max(1, s.view.H-1) - s.top)
}

// Selected is the selected text, without decoration. A row that continues
// the line above is joined to it.
func (s *Scrollbox) Selected() string {
	if !s.sel.on {
		return ""
	}
	a, b := s.order()
	var out strings.Builder
	for ri := a.row; ri <= b.row && ri < len(s.rows); ri++ {
		if ri > a.row && !s.rows[ri].Cont {
			out.WriteByte('\n')
		}
		x := 0
		for _, g := range s.rows[ri].Gs {
			if !g.Deco && !g.Spin && s.picked(ri, x, g.W, a, b) {
				out.WriteString(g.G)
			}
			x += g.W
		}
	}
	return out.String()
}

func (s *Scrollbox) ClearSelection() { s.sel.on = false }
