package term

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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

// Graphemes calls fn with each grapheme of s as it shows, and its width, 1
// or 2. Zero width clusters, like a combining mark with no base, are
// skipped. Where terminals disagree with the Unicode tables, the grapheme is
// changed to one they agree on (see safe), or else given 2 cells: the
// renderer blanks both first, so a terminal that draws it 1 wide leaves a
// blank cell, where 1 cell drawn 2 wide would cover its neighbor.
func Graphemes(s string, fn func(g string, w int)) {
	state := -1
	s = safe(s)
	for s != "" {
		var g string
		var w int
		g, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		r, _ := utf8.DecodeRuneInString(g)
		switch {
		case w == 0:
			continue
		case strings.ContainsRune(g, 0xFE0F): // emoji presentation: 2 cells in Windows Terminal and tmux, even after "a" or a keycap
			w = 2
		case r >= 0x1FA70 && r <= 0x1FAFF: // emoji newer than uniseg's tables
			w = 2
		}
		fn(g, min(w, 2))
	}
}

// safe is s with what moves the cursor unpredictably made visible or taken
// out. Control characters show as their Control Pictures, and C1 controls,
// invalid bytes and unassigned code points as U+FFFD. A tab is 4 spaces.
// Format characters go but the zero width joiner: bidi controls, zero width
// spaces, soft hyphens, tags; terminals give them 0 or 1 cells. So do text
// presentation selectors, and skin tones on what is not a wide emoji.
func safe(s string) string {
	plain := true
	for i := 0; i < len(s) && plain; i++ {
		plain = s[i] >= 0x20 && s[i] < 0x7f
	}
	if plain {
		return s
	}
	var b strings.Builder
	wide := false // the rune before is a wide emoji or an emoji presentation selector
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7f:
			b.WriteRune(0x2421)
		case r == 0xFE0E, r != 0x200D && unicode.Is(unicode.Cf, r):
		case r >= 0x1F3FB && r <= 0x1F3FF && !wide:
		case unicode.IsGraphic(r), unicode.Is(unicode.Co, r), r == 0x200D:
			b.WriteRune(r)
		default:
			b.WriteRune(utf8.RuneError)
		}
		wide = r == 0xFE0F || uniseg.StringWidth(string(r)) == 2
	}
	return b.String()
}

// Width is the number of cells s takes.
func Width(s string) int {
	n := 0
	Graphemes(s, func(_ string, w int) { n += w })
	return n
}
