package term

import (
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
)

// Renderer draws buffers on the terminal, writing only the cells that differ
// from what is on screen, as one synchronized frame.
type Renderer struct {
	t     *Terminal
	front *Buffer
}

func NewRenderer(t *Terminal) *Renderer { return &Renderer{t: t} }

// Render makes the screen show b. A buffer of a new size repaints it all.
func (r *Renderer) Render(b *Buffer) {
	var o strings.Builder
	if r.front == nil || r.front.W != b.W || r.front.H != b.H {
		o.WriteString("\x1b[0m\x1b[2J")
		r.front = &Buffer{W: b.W, H: b.H, Cells: make([]Cell, len(b.Cells))} // no cell matches
	}
	pen, penSet := Style{}, false
	cx, cy := -1, -1
	for y := range b.H {
		row, front := b.Cells[y*b.W:(y+1)*b.W], r.front.Cells[y*b.W:(y+1)*b.W]
		// A terminal may measure a grapheme beyond ASCII other than the tables
		// do, and draw over its neighbor. So a row that has one is drawn whole
		// when it changes, and the cursor is placed again after each.
		whole := !slices.Equal(row, front) && slices.ContainsFunc(row, func(c Cell) bool { return len(c.G) > 1 })
		for x := 0; x < b.W; x++ {
			c := row[x]
			if c.W == 0 || !whole && shown(row, front, x) {
				continue
			}
			if !penSet || c.Style != pen {
				o.WriteString(r.sgr(c.Style))
				pen, penSet = c.Style, true
			}
			if cx != x || cy != y {
				o.WriteString(cup(x, y))
			}
			if c.W == 2 { // blank both cells first, for a terminal that draws it 1 wide
				o.WriteString("  " + cup(x, y))
			}
			o.WriteString(c.G)
			cx, cy = x+int(c.W), y
			if len(c.G) > 1 {
				cx = -1
			}
		}
	}
	changed := o.Len() > 0
	if b.Cursor {
		o.WriteString(cup(b.CursorX, b.CursorY) + "\x1b[?25h")
	} else {
		o.WriteString("\x1b[?25l")
	}
	if !changed && b.Cursor == r.front.Cursor && b.CursorX == r.front.CursorX && b.CursorY == r.front.CursorY {
		return
	}
	copy(r.front.Cells, b.Cells)
	r.front.Cursor, r.front.CursorX, r.front.CursorY = b.Cursor, b.CursorX, b.CursorY
	r.t.Write("\x1b[?2026h\x1b[?25l" + o.String() + "\x1b[?2026l")
	r.t.Flush()
}

// shown is whether the cell at x, with the right half of a wide one, is on the
// screen already.
func shown(row, front []Cell, x int) bool {
	return row[x] == front[x] && (row[x].W != 2 || x+1 >= len(row) || row[x+1] == front[x+1])
}

func cup(x, y int) string { return "\x1b[" + strconv.Itoa(y+1) + ";" + strconv.Itoa(x+1) + "H" }

// Invalidate makes the next frame repaint everything.
func (r *Renderer) Invalidate() { r.front = nil }

func (r *Renderer) sgr(s Style) string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	for _, a := range []struct {
		a Attr
		s string
	}{{Bold, ";1"}, {Faint, ";2"}, {Italic, ";3"}, {Underline, ";4"}} {
		if s.Attr&a.a != 0 {
			b.WriteString(a.s)
		}
	}
	r.color(&b, s.FG, "38")
	r.color(&b, s.BG, "48")
	b.WriteByte('m')
	return b.String()
}

func (r *Renderer) color(b *strings.Builder, c Color, kind string) {
	if c == 0 {
		return
	}
	cr, cg, cb := c.rgb()
	if r.t.Caps.Truecolor {
		b.WriteString(";" + kind + ";2;" + strconv.Itoa(int(cr)) + ";" + strconv.Itoa(int(cg)) + ";" + strconv.Itoa(int(cb)))
		return
	}
	b.WriteString(";" + kind + ";5;" + strconv.Itoa(xterm256(cr, cg, cb)))
}

// xterm256 is the nearest of the 6x6x6 cube and the gray ramp.
func xterm256(r, g, b uint8) int {
	steps := [6]int{0, 95, 135, 175, 215, 255}
	near := func(v uint8) int {
		best := 0
		for i, s := range steps {
			if abs(int(v)-s) < abs(int(v)-steps[best]) {
				best = i
			}
		}
		return best
	}
	ri, gi, bi := near(r), near(g), near(b)
	cube := 16 + 36*ri + 6*gi + bi
	dc := sq(int(r)-steps[ri]) + sq(int(g)-steps[gi]) + sq(int(b)-steps[bi])
	gi2 := grayStep(r, g, b)
	gv := 8 + 10*gi2
	dg := sq(int(r)-gv) + sq(int(g)-gv) + sq(int(b)-gv)
	if dg < dc {
		return 232 + gi2
	}
	return cube
}

// grayStep is the nearest step of the 24-step gray ramp, 8 to 238 by 10.
func grayStep(r, g, b uint8) int {
	return min(23, max(0, ((int(r)+int(g)+int(b))/3-3)/10))
}

// Apart256 is c, or where c and bg share one 256-color index, the gray one
// step from bg's toward c's lightness, or inward at an end of the ramp.
func Apart256(c, bg Color) Color {
	cr, cg, cb := c.rgb()
	br, bgr, bb := bg.rgb()
	if xterm256(cr, cg, cb) != xterm256(br, bgr, bb) {
		return c
	}
	step, d := grayStep(br, bgr, bb), -1
	if int(cr)+int(cg)+int(cb) >= int(br)+int(bgr)+int(bb) {
		d = 1
	}
	if step+d < 0 || step+d > 23 {
		d = -d // at an end of the ramp: step inward
	}
	step += d
	v := uint8(8 + 10*step)
	return RGB(v, v, v)
}

func abs(v int) int { return max(v, -v) }
func sq(v int) int  { return v * v }

// Copy puts s on the system clipboard through the terminal (OSC 52), which
// also works over ssh.
func (t *Terminal) Copy(s string) {
	t.Write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07")
	t.Flush()
}
