package tui

import (
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

// MenuItem is one row of a menu, under its section's title.
type MenuItem struct {
	Label, Hint, Section string
	On                   bool // checked, in a menu of checks
	Pick                 func(a *App, it *MenuItem)
}

// Menu is a list to search by typing and pick from with Enter or a click.
// The row under the mouse is selected.
// A menu of checks stays open and toggles with Space or Enter; any other
// closes on a pick. One that is Single too checks one item per section, and
// an item with no section in it is no check but an action that closes it.
type Menu struct {
	Items  []MenuItem
	Checks bool
	Single bool

	query string
	sel   int // index into rows
	top   int
	h     int
	rows  []mrow
	layer *Layer
}

// mrow is a section's title, or an item, by index.
type mrow struct {
	title string
	item  int
}

// OpenMenu shows m in a dialog.
func (a *App) OpenMenu(title string, m *Menu) {
	m.filter()
	m.layer = a.Dialog(title, &Node{View: m})
}

// filter shows the items that hold the query, those that start with it
// first.
func (m *Menu) filter() {
	q := strings.ToLower(m.query)
	var first, rest []int
	for i, it := range m.Items {
		l, h := strings.ToLower(it.Label), strings.TrimPrefix(strings.ToLower(it.Hint), "/")
		switch {
		case strings.HasPrefix(l, q) || strings.HasPrefix(h, q):
			first = append(first, i)
		case strings.Contains(l+" "+h+" "+strings.ToLower(it.Section), q):
			rest = append(rest, i)
		}
	}
	m.rows, m.sel, m.top = nil, -1, 0
	section := ""
	for _, i := range append(first, rest...) {
		it := m.Items[i]
		if it.Section != section {
			section = it.Section
			if len(m.rows) > 0 {
				m.rows = append(m.rows, mrow{item: -1})
			}
			if section != "" {
				m.rows = append(m.rows, mrow{title: section, item: -1})
			}
		}
		if m.sel < 0 {
			m.sel = len(m.rows)
		}
		m.rows = append(m.rows, mrow{item: i})
	}
}

// Height is the search row, a blank, and the rows; at least one row, for
// the word that nothing matched.
func (m *Menu) Height(int) int { return 2 + max(1, len(m.rows)) }

func (m *Menu) Draw(a *App, c *Canvas, r Rect) {
	muted := a.Theme.muted()
	if m.query == "" {
		c.Text(r.X, r.Y, "Search", muted)
	} else {
		x := c.Text(r.X, r.Y, m.query, a.Theme.text())
		c.Cursor(x, r.Y)
	}
	m.h = max(1, r.H-2)
	if len(m.rows) == 0 {
		c.Text(r.X, r.Y+2, "No match", muted)
		return
	}
	if m.sel >= 0 {
		m.top = min(max(m.top, m.sel-m.h+1), m.sel)
	}
	for y := 0; y < m.h && m.top+y < len(m.rows); y++ {
		row, ry := m.rows[m.top+y], r.Y+2+y
		if row.item < 0 {
			c.Text(r.X, ry, row.title, term.Style{FG: a.Theme.Accent, Attr: term.Bold})
			continue
		}
		it := m.Items[row.item]
		st, hint := a.Theme.text(), muted
		if m.top+y == m.sel {
			c.Fill(Rect{r.X - 1, ry, r.W + 2, 1}, a.Theme.Accent)
			st = term.Style{FG: a.Theme.BG, BG: a.Theme.Accent}
			hint = st
		}
		label := it.Label
		if m.Checks && (!m.Single || it.Section != "") {
			mark := "  "
			if it.On {
				mark = "✓ "
			}
			label = mark + label
		}
		gs := spread(run(label, st), run(it.Hint, hint), r.W)
		x := r.X
		for _, g := range gs {
			c.Set(x, ry, g.G, g.W, st)
			if g.St.FG != 0 {
				c.Restyle(x, ry, func(s *term.Style) { s.FG = g.St.FG })
			}
			x += g.W
		}
	}
}

// step moves the selection by n items, past the section titles.
func (m *Menu) step(n int) {
	dir := 1
	if n < 0 {
		dir, n = -1, -n
	}
	for i := m.sel; n > 0; {
		i += dir
		if i < 0 || i >= len(m.rows) {
			return
		}
		if m.rows[i].item >= 0 {
			m.sel, n = i, n-1
		}
	}
}

// page moves the selection by n rows, to the nearest item on the way.
func (m *Menu) page(n int) {
	if len(m.rows) == 0 {
		return
	}
	i := min(max(0, m.sel+n), len(m.rows)-1)
	for d := 0; d < len(m.rows); d++ {
		for _, j := range []int{i - d, i + d} {
			if j >= 0 && j < len(m.rows) && m.rows[j].item >= 0 {
				m.sel = j
				return
			}
		}
	}
}

func (m *Menu) pick(a *App, row int) {
	if row < 0 || row >= len(m.rows) || m.rows[row].item < 0 {
		return
	}
	it := &m.Items[m.rows[row].item]
	switch {
	case m.Single && it.Section != "":
		for i := range m.Items {
			if m.Items[i].Section == it.Section {
				m.Items[i].On = &m.Items[i] == it
			}
		}
	case m.Checks && !m.Single:
		it.On = !it.On
	default:
		a.Pop(m.layer)
	}
	if it.Pick != nil {
		it.Pick(a, it)
	}
}

func (m *Menu) Key(a *App, k term.Key) bool {
	switch k.String() {
	case "up":
		m.step(-1)
	case "down":
		m.step(1)
	case "pgup":
		m.page(-m.h)
	case "pgdn":
		m.page(m.h)
	case "enter":
		m.pick(a, m.sel)
	case "space":
		if m.Checks {
			m.pick(a, m.sel)
			break
		}
		m.query += " "
		m.filter()
	case "backspace":
		if m.query != "" {
			rs := []rune(m.query)
			m.query = string(rs[:len(rs)-1])
			m.filter()
		}
	default:
		t := k.Text()
		if t == "" {
			return false
		}
		m.query += t
		m.filter()
	}
	return true
}

// Paste searches for the pasted text's first line.
func (m *Menu) Paste(_ *App, t string) {
	line, _, _ := strings.Cut(t, "\n")
	m.query += line
	m.filter()
}

func (m *Menu) Mouse(a *App, mo term.Mouse, r Rect) bool {
	switch {
	case mo.Button == term.WheelUp:
		m.step(-1)
	case mo.Button == term.WheelDown:
		m.step(1)
	case mo.Action == term.Press && mo.Button == term.Left:
		if row := m.top + mo.Y - r.Y - 2; mo.Y >= r.Y+2 && row < len(m.rows) {
			m.sel = row
			m.pick(a, row)
		}
	case mo.Action == term.Motion && mo.Button == term.NoButton:
		if row := m.top + mo.Y - r.Y - 2; mo.Y >= r.Y+2 && row < len(m.rows) && m.rows[row].item >= 0 {
			m.sel = row
		}
	default:
		return false
	}
	return true
}
