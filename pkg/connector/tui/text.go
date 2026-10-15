package tui

import (
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// wrap splits n items of the given widths into rows no wider than w,
// breaking after the last space in a row where there is one. hard(i) ends a
// row after item i. Rows are [start, end) item ranges; there is always one.
func wrap(n int, width func(int) int, space, hard func(int) bool, w int) [][2]int {
	var rows [][2]int
	start, x, brk := 0, 0, -1
	for i := range n {
		iw := width(i)
		if x+iw > w && i > start {
			end := i
			if brk > start {
				end = brk
			}
			rows = append(rows, [2]int{start, end})
			start, x, brk = end, 0, -1
			for j := start; j < i; j++ {
				x += width(j)
			}
		}
		x += iw
		if space(i) {
			brk = i + 1
		}
		if hard(i) {
			rows = append(rows, [2]int{start, i + 1})
			start, x, brk = i+1, 0, -1
		}
	}
	return append(rows, [2]int{start, n})
}

type glyph struct {
	g string
	w int
}

func glyphs(s string) []glyph {
	var gs []glyph
	term.Graphemes(s, func(g string, w int) { gs = append(gs, glyph{g, w}) })
	return gs
}

// wrapLine is the rows of one line of text at width w.
func wrapLine(gs []glyph, w int) [][2]int {
	return wrap(len(gs), func(i int) int { return gs[i].w }, func(i int) bool { return gs[i].g == " " }, func(int) bool { return false }, w)
}

// Text is wrapped text, left aligned or centered.
type Text struct {
	S      string
	Style  term.Style
	Center bool
}

func (t *Text) rows(w int) [][]glyph {
	var out [][]glyph
	for _, line := range strings.Split(t.S, "\n") {
		gs := glyphs(line)
		for _, r := range wrapLine(gs, max(1, w)) {
			out = append(out, gs[r[0]:r[1]])
		}
	}
	return out
}

func (t *Text) Height(w int) int { return len(t.rows(w)) }

func (t *Text) Draw(a *App, c *Canvas, r Rect) {
	for i, row := range t.rows(r.W) {
		x := r.X
		if t.Center {
			w := 0
			for _, g := range row {
				w += g.w
			}
			x += max(0, (r.W-w)/2)
		}
		for _, g := range row {
			c.Set(x, r.Y+i, g.g, g.w, t.Style)
			x += g.w
		}
	}
}

var wordmark = strings.Split(strings.TrimSuffix(version.Wordmark, "\n"), "\n")

// Wordmark draws the wordmark centered, or the plain name where it does not
// fit.
type Wordmark struct{}

func (Wordmark) Height(int) int { return len(wordmark) }
func (Wordmark) Draw(a *App, c *Canvas, r Rect) {
	w := term.Width(wordmark[0])
	for _, l := range wordmark {
		w = max(w, term.Width(l))
	}
	st := term.Style{FG: a.Theme.Text, Attr: term.Bold}
	if r.W < w {
		c.Text(r.X+max(0, (r.W-9)/2), r.Y+len(wordmark)/2, "mainplane", st)
		return
	}
	for i, l := range wordmark {
		c.Text(r.X+(r.W-w)/2, r.Y+i, l, st)
	}
}

// Glyphs is one line of styled glyphs.
type Glyphs struct{ Gs []Glyph }

func (g *Glyphs) Height(int) int { return 1 }

func (g *Glyphs) Draw(a *App, c *Canvas, r Rect) {
	x := r.X
	for _, gl := range g.Gs {
		c.Set(x, r.Y, gl.G, gl.W, gl.St)
		x += gl.W
	}
}
