package term

import (
	"strconv"
	"strings"

	"github.com/rivo/uniseg"
)

// Color is a 24-bit color, or 0 for the terminal's own default.
type Color uint32

const rgbFlag = 1 << 24

func RGB(r, g, b uint8) Color { return rgbFlag | Color(r)<<16 | Color(g)<<8 | Color(b) }

// Hex is a color written #rrggbb.
func Hex(s string) Color {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	if err != nil || len(s) != 7 || s[0] != '#' {
		panic("term: bad color " + s)
	}
	return rgbFlag | Color(v)
}

func (c Color) rgb() (r, g, b uint8) { return uint8(c >> 16), uint8(c >> 8), uint8(c) }

// Blend moves c toward o by t, 0 to 1. The default color does not blend.
func (c Color) Blend(o Color, t float64) Color {
	if c == 0 || o == 0 {
		return c
	}
	r1, g1, b1 := c.rgb()
	r2, g2, b2 := o.rgb()
	mix := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5) }
	return RGB(mix(r1, r2), mix(g1, g2), mix(b1, b2))
}

type Attr uint8

const (
	Bold Attr = 1 << iota
	Faint
	Italic
	Underline
)

type Style struct {
	FG, BG Color
	Attr   Attr
}

// Cell is one grapheme and its width. The cell right of a wide grapheme
// holds width 0 and no grapheme.
type Cell struct {
	G string
	W int8
	Style
}

// Buffer is a grid of cells and where the cursor shows, if it does.
type Buffer struct {
	W, H             int
	Cells            []Cell
	CursorX, CursorY int
	Cursor           bool
}

func NewBuffer(w, h int) *Buffer {
	b := &Buffer{W: w, H: h, Cells: make([]Cell, w*h)}
	for i := range b.Cells {
		b.Cells[i] = Cell{G: " ", W: 1}
	}
	return b
}

func (b *Buffer) At(x, y int) *Cell {
	if x < 0 || y < 0 || x >= b.W || y >= b.H {
		return nil
	}
	return &b.Cells[y*b.W+x]
}

// Set puts grapheme g of width w at x, y. A wide grapheme that does not fit
// is a space, and a wide grapheme cut by this one is erased.
func (b *Buffer) Set(x, y int, g string, w int, s Style) {
	c := b.At(x, y)
	if c == nil {
		return
	}
	if c.W == 0 { // the right half of a wide grapheme
		if l := b.At(x-1, y); l != nil {
			*l = Cell{G: " ", W: 1, Style: l.Style}
		}
	}
	if old := b.At(x+1, y); c.W == 2 && old != nil {
		*old = Cell{G: " ", W: 1, Style: old.Style}
	}
	if w == 2 && x+1 >= b.W {
		g, w = " ", 1
	}
	*c = Cell{G: g, W: int8(w), Style: s}
	if w == 2 {
		r := b.At(x+1, y)
		if r.W == 2 {
			if rr := b.At(x+2, y); rr != nil {
				*rr = Cell{G: " ", W: 1, Style: rr.Style}
			}
		}
		*r = Cell{W: 0, Style: s}
	}
}

// Graphemes calls fn with each grapheme of s and its width, 1 or 2. Zero
// width clusters, like a lone control byte, are skipped.
func Graphemes(s string, fn func(g string, w int)) {
	state := -1
	for s != "" {
		var g string
		var w int
		g, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		if w > 0 {
			fn(g, min(w, 2))
		}
	}
}

// Width is the number of cells s takes.
func Width(s string) int {
	n := 0
	Graphemes(s, func(_ string, w int) { n += w })
	return n
}
