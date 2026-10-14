package term

import (
	"encoding/base64"
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
		for x := 0; x < b.W; x++ {
			i := y*b.W + x
			c := b.Cells[i]
			if c.W == 0 || c == r.front.Cells[i] && (c.W != 2 || x+1 >= b.W || b.Cells[i+1] == r.front.Cells[i+1]) {
				continue
			}
			if cx != x || cy != y {
				o.WriteString("\x1b[" + strconv.Itoa(y+1) + ";" + strconv.Itoa(x+1) + "H")
			}
			if !penSet || c.Style != pen {
				o.WriteString(r.sgr(c.Style))
				pen, penSet = c.Style, true
			}
			o.WriteString(c.G)
			cx, cy = x+int(c.W), y
		}
	}
	changed := o.Len() > 0
	if b.Cursor {
		o.WriteString("\x1b[" + strconv.Itoa(b.CursorY+1) + ";" + strconv.Itoa(b.CursorX+1) + "H\x1b[?25h")
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
	avg := (int(r) + int(g) + int(b)) / 3
	gi2 := min(23, max(0, (avg-8+5)/10))
	gv := 8 + 10*gi2
	dg := sq(int(r)-gv) + sq(int(g)-gv) + sq(int(b)-gv)
	if dg < dc {
		return 232 + gi2
	}
	return cube
}

func abs(v int) int { return max(v, -v) }
func sq(v int) int  { return v * v }

// Copy puts s on the system clipboard through the terminal (OSC 52), which
// also works over ssh.
func (t *Terminal) Copy(s string) {
	t.Write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07")
	t.Flush()
}
