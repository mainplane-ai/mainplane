package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/rivo/uniseg"
)

// collapseLines is the size of a paste shown as one placeholder.
const collapseLines = 3

// elem is one grapheme of the input, a line break ("\n", width 0), or a
// collapsed paste: its label shown, its text kept.
type elem struct {
	g     string
	w     int
	paste string
}

func (e elem) space() bool { return e.g == " " || e.g == "\t" || e.g == "\n" }

// Input is a multi-line text input. It wraps, grows to MaxRows and then
// scrolls. Shift+arrows select. A paste of collapseLines or more lines is one
// placeholder until a click expands it; Enter sends it as it is. Before sees
// each key first and takes it by returning true.
type Input struct {
	Placeholder string
	Cursor      bool // show the cursor without focus, as in a dialog
	MaxRows     int
	OnSubmit    func(a *App, text string)
	OnKey       func(a *App) // after each key the input takes
	Before      func(a *App, k term.Key) bool

	els    []elem
	cur    int
	undo   []snap // the input before each change, newest last
	redo   []snap // the input before each undo, newest last; a change clears it
	typing bool   // the last change typed a word's character: the next typed joins it
	anchor int    // the other end of the selection; -1 is none
	top    int
	view   Rect
}

// snap is the input at one moment, for Ctrl+Z.
type snap struct {
	els []elem
	cur int
}

func NewInput() *Input { return &Input{anchor: -1, MaxRows: 1} }

// rows wraps at w less one column, where the cursor sits after a full row.
func (s *Input) rows(w int) [][2]int {
	return wrap(len(s.els), func(i int) int { return s.els[i].w }, func(i int) bool { return s.els[i].space() }, func(i int) bool { return s.els[i].g == "\n" }, max(1, w-1))
}

func (s *Input) Height(w int) int { return min(max(1, len(s.rows(w))), max(1, s.MaxRows)) }

// at is the row and column of index i.
func (s *Input) at(rows [][2]int, i int) (int, int) {
	for r, row := range rows {
		if i < row[1] || r == len(rows)-1 {
			col := 0
			for j := row[0]; j < i && j < row[1]; j++ {
				col += s.els[j].w
			}
			return r, col
		}
	}
	return 0, 0
}

// index is the index at row r, column col: before the element that covers
// col, or the row's end.
func (s *Input) index(rows [][2]int, r, col int) int {
	r = min(max(0, r), len(rows)-1)
	row, x := rows[r], 0
	end := row[1]
	if r < len(rows)-1 && end > row[0] && s.els[end-1].space() {
		end-- // before the break or the space the row wrapped at
	}
	for i := row[0]; i < end; i++ {
		if x+s.els[i].w > col {
			return i
		}
		x += s.els[i].w
	}
	return end
}

func (s *Input) Text() string {
	var b strings.Builder
	for _, e := range s.els {
		if e.paste != "" {
			b.WriteString(e.paste)
		} else {
			b.WriteString(e.g)
		}
	}
	return b.String()
}

func (s *Input) Clear() {
	s.els, s.cur, s.anchor, s.top, s.undo, s.redo, s.typing = nil, 0, -1, 0, nil, nil, false
}

// undoMax is how many changes Ctrl+Z goes back: each keeps a copy of the
// input, which a paste can make large.
const undoMax = 100

// remember keeps the input before a change for Ctrl+Z.
func (s *Input) remember(before snap) {
	s.undo, s.redo = append(s.undo, before), nil
	if len(s.undo) > undoMax {
		s.undo = s.undo[1:]
	}
}

// restore puts back the newest input of from, keeping the input it replaces
// in to: an undo, or with the stacks swapped a redo.
func (s *Input) restore(from, to *[]snap) {
	n := len(*from)
	if n == 0 {
		return
	}
	*to = append(*to, snap{slices.Clone(s.els), s.cur})
	last := (*from)[n-1]
	*from = (*from)[:n-1]
	s.els, s.cur, s.anchor, s.typing = last.els, last.cur, -1, false
}

func (s *Input) span() (int, int) {
	if s.anchor < 0 {
		return s.cur, s.cur
	}
	return min(s.anchor, s.cur), max(s.anchor, s.cur)
}

func (s *Input) Selected() string {
	a, b := s.span()
	sub := Input{els: s.els[a:b]}
	return sub.Text()
}

func (s *Input) ClearSelection() { s.anchor = -1 }

func (s *Input) deleteSelection() bool {
	a, b := s.span()
	if a == b {
		s.anchor = -1
		return false
	}
	s.els = append(s.els[:a], s.els[b:]...)
	s.cur, s.anchor = a, -1
	return true
}

func (s *Input) insert(es ...elem) {
	s.redo = nil // a change: what was undone stays undone
	s.deleteSelection()
	s.els = append(s.els[:s.cur], append(es, s.els[s.cur:]...)...)
	s.cur += len(es)
}

// textElems is t as elements. Each keeps its grapheme as typed, so the text
// sends as typed, and is as wide as it shows.
func textElems(t string) []elem {
	var es []elem
	for i, line := range strings.Split(t, "\n") {
		if i > 0 {
			es = append(es, elem{g: "\n"})
		}
		for state := -1; line != ""; {
			var g string
			g, line, _, state = uniseg.FirstGraphemeClusterInString(line, state)
			es = append(es, elem{g: g, w: term.Width(g)})
		}
	}
	return es
}

// Attach puts text at the cursor collapsed under label, as a paste.
func (s *Input) Attach(label, text string) {
	s.insert(elem{g: label, w: term.Width(label), paste: text})
}

func (s *Input) Paste(a *App, t string) {
	s.remember(snap{slices.Clone(s.els), s.cur})
	s.typing = false
	if n := strings.Count(t, "\n") + 1; n >= collapseLines {
		label := fmt.Sprintf("[Pasted %d lines]", n)
		s.insert(elem{g: label, w: term.Width(label), paste: t})
		return
	}
	s.insert(textElems(t)...)
}

// expand makes the paste at i editable text.
func (s *Input) expand(i int) {
	es := textElems(s.els[i].paste)
	s.els = append(s.els[:i], append(es, s.els[i+1:]...)...)
	s.cur, s.anchor = i+len(es), -1
}

// move puts the cursor at i, extending the selection when select is set.
func (s *Input) move(a *App, i int, sel bool) {
	if sel && s.anchor < 0 {
		s.anchor = s.cur
	}
	if !sel {
		s.anchor = -1
	}
	s.cur = min(max(0, i), len(s.els))
	if s.anchor == s.cur {
		s.anchor = -1
	}
	if s.anchor >= 0 {
		a.Select(s)
	}
}

func (s *Input) word(dir int) int {
	i := s.cur
	if dir < 0 {
		for i > 0 && s.els[i-1].space() {
			i--
		}
		for i > 0 && !s.els[i-1].space() {
			i--
		}
		return i
	}
	for i < len(s.els) && s.els[i].space() {
		i++
	}
	for i < len(s.els) && !s.els[i].space() {
		i++
	}
	return i
}

// Key edits the input. Ctrl+Z undoes a change; a word typed, with the space
// after it, is one change. Ctrl+Y redoes, and Ctrl+Shift+Z where the
// terminal tells it from Ctrl+Z: with the kitty protocol, not in Windows
// Terminal, whose console sends both as the same byte.
func (s *Input) Key(a *App, k term.Key) bool {
	if s.Before != nil && s.Before(a, k) {
		return true
	}
	if k := k.String(); k == "ctrl+z" || k == "ctrl+shift+z" || k == "ctrl+y" {
		if k == "ctrl+z" {
			s.restore(&s.undo, &s.redo)
		} else {
			s.restore(&s.redo, &s.undo)
		}
		if s.OnKey != nil {
			s.OnKey(a)
		}
		return true
	}
	before, selected := snap{slices.Clone(s.els), s.cur}, s.anchor >= 0
	took := s.edit(a, k)
	switch {
	case slices.Equal(before.els, s.els):
		s.typing = false // a move: what is typed next is a change of its own
	case s.els == nil: // sent, and Clear forgot the undo
	default:
		t := k.Text() // a space joins the word before it, and the next word starts anew
		typed := t != "" && !strings.Contains(t, "\n")
		if !typed || !s.typing || selected {
			s.remember(before)
		}
		s.typing = typed && strings.TrimSpace(t) == t
	}
	return took
}

func (s *Input) edit(a *App, k term.Key) bool {
	rows := s.rows(s.view.W)
	r, col := s.at(rows, s.cur)
	name := k.String()
	sel := strings.Contains(name, "shift+")
	switch strings.Replace(name, "shift+", "", 1) {
	case "enter":
		if sel {
			s.insert(elem{g: "\n"})
		} else if t := s.Text(); strings.TrimSpace(t) != "" && s.OnSubmit != nil {
			s.Clear()
			s.OnSubmit(a, t)
		}
	case "ctrl+j", "alt+enter":
		s.insert(elem{g: "\n"})
	case "backspace":
		if !s.deleteSelection() && s.cur > 0 {
			s.els = append(s.els[:s.cur-1], s.els[s.cur:]...)
			s.cur--
		}
	case "super+backspace":
		if !s.deleteSelection() {
			h := s.index(rows, r, 0)
			s.els = append(s.els[:h], s.els[s.cur:]...)
			s.cur = h
		}
	case "ctrl+backspace", "alt+backspace", "ctrl+w":
		if !s.deleteSelection() {
			w := s.word(-1)
			s.els = append(s.els[:w], s.els[s.cur:]...)
			s.cur = w
		}
	case "delete":
		if !s.deleteSelection() && s.cur < len(s.els) {
			s.els = append(s.els[:s.cur], s.els[s.cur+1:]...)
		}
	case "ctrl+delete", "alt+delete":
		if !s.deleteSelection() {
			s.els = append(s.els[:s.cur], s.els[s.word(1):]...)
		}
	case "ctrl+x", "super+x":
		if t := s.Selected(); t != "" {
			a.Copy(t)
			s.deleteSelection()
		}
	case "left":
		s.move(a, s.cur-1, sel)
	case "right":
		s.move(a, s.cur+1, sel)
	case "ctrl+left", "alt+left":
		s.move(a, s.word(-1), sel)
	case "ctrl+right", "alt+right":
		s.move(a, s.word(1), sel)
	case "up":
		s.move(a, s.index(rows, r-1, col), sel)
	case "down":
		s.move(a, s.index(rows, r+1, col), sel)
	case "home", "super+left":
		s.move(a, s.index(rows, r, 0), sel)
	case "end", "super+right":
		s.move(a, s.index(rows, r, 1<<30), sel)
	default:
		t := k.Text()
		if t == "" {
			return false
		}
		s.insert(textElems(t)...)
	}
	if s.OnKey != nil {
		s.OnKey(a)
	}
	return true
}

func (s *Input) Mouse(a *App, m term.Mouse, r Rect) bool {
	rows := s.rows(r.W)
	i := s.index(rows, s.top+m.Y-r.Y, m.X-r.X)
	switch {
	case m.Button == term.WheelUp:
		s.top = max(0, s.top-1)
	case m.Button == term.WheelDown:
		s.top = min(s.top+1, max(0, len(rows)-r.H))
	case m.Action == term.Press && m.Button == term.Left:
		a.Select(s)
		s.typing = false
		if i < len(s.els) && s.els[i].paste != "" {
			s.remember(snap{slices.Clone(s.els), s.cur})
			s.expand(i)
			break
		}
		s.cur, s.anchor = i, i
	case m.Action == term.Motion && m.Button == term.Left:
		s.cur = i
		if s.anchor != s.cur {
			a.Select(s)
		}
	case m.Action == term.Release:
		if s.anchor == s.cur {
			s.anchor = -1
		}
	default:
		return false
	}
	return true
}

func (s *Input) Draw(a *App, c *Canvas, r Rect) {
	s.view = r
	rows := s.rows(r.W)
	cr, cc := s.at(rows, s.cur)
	s.top = min(max(s.top, cr-r.H+1), cr)
	s.top = max(0, min(s.top, len(rows)-r.H))
	if len(s.els) == 0 {
		c.Text(r.X, r.Y, s.Placeholder, a.Theme.muted())
	}
	sa, sb := s.span()
	for y := 0; y < r.H && s.top+y < len(rows); y++ {
		row, x := rows[s.top+y], r.X
		for i := row[0]; i < row[1]; i++ {
			e := s.els[i]
			if e.g == "\n" {
				continue
			}
			st, g := a.Theme.text(), e.g
			if e.paste != "" {
				st = term.Style{FG: a.Theme.Accent, Attr: term.Bold}
			}
			if i >= sa && i < sb {
				st.BG = a.Theme.Selection
			}
			x = c.Text(x, r.Y+y, g, st)
		}
	}
	if s.Cursor || a.focusView() == View(s) {
		c.Cursor(r.X+cc, r.Y+cr-s.top)
	}
}
