package tui

import (
	_ "embed"
	"encoding/binary"
	"math"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

// The canon biplane on the Hello screen, drawn live as a braille dither: a
// rim light, a Bayer dither that holds its dots against flicker, outlines
// and creases as more dots of the same ink, the paint in the accent. It
// turns toward the mouse, and a key flies it off past the camera. The
// design repo's splash viewer is its prototype; the values are picked there.
const (
	planeFrame     = time.Second / 30
	planeAzimuth   = 32.0 // degrees: the base pose
	planeElevation = 12.0
	planeProp      = 3.5 // propeller turns a second
	planeSize      = 1.06
	planeFillS     = 0.85 // share of the width at 60 columns, growing to planeFillL at 250
	planeFillL     = 1.0
	planeFillH     = 2.0  // share of the height
	planeTilt      = 20.0 // degrees toward the mouse at planeReach
	planeReach     = 1.5  // the pointer's distance from the center, in half screens, that gives the full tilt
	planeEase      = 0.48 // seconds the pose takes to follow the mouse
	planeStrength  = 0.84 // the inks, from the screen's color toward the text's and the accent's
	planeDmax      = 1.0  // the most dots a face gets
	planeRim       = 4.0  // higher is a thinner rim of light
	planeAmbient   = 0.2
	planeHold      = 0.1  // the change in density a dot needs to flip
	planeCrease    = 20.0 // degrees between faces that draw a line
	planeDz        = 1.0  // model units of depth jump that draw an outline
	flyDur         = 2.2  // seconds from the key until the plane is gone
	flyBank        = 45.0
	flyBankT       = 0.8 // the share of flyDur the bank takes
	flyTurn        = 41.0
	flyTurnT       = 0.5
	flyClimb       = -1.0
	flyGo          = 0.25 // the share of flyDur when it speeds toward the camera
	flyDist        = 28.0 // model units it travels
	flyLens        = 22.0 // the camera's distance at full perspective: smaller is more dramatic
	flyProp        = 2.0  // the propeller's speed at full bank
	flyAfter       = 0.2  // seconds from gone to the next screen
)

// planeSX, planeSY: pixels per cell. A dot is 2x2 of them, so braille's 2x4
// dots are square on a cell twice as tall as wide.
const planeSX, planeSY, rad = 4, 8, math.Pi / 180

// plane.bin is the canon model, baked by the design repo's splash/bake.js.
//
//go:embed plane.bin
var planeBin []byte

type vec [3]float64

func (a vec) sub(b vec) vec     { return vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a vec) dot(b vec) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a vec) cross(b vec) vec {
	return vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

func (a vec) norm() vec {
	l := math.Sqrt(a.dot(a))
	if l == 0 {
		l = 1
	}
	return vec{a[0] / l, a[1] / l, a[2] / l}
}

// mat is a 3x3 rotation, column major.
type mat [9]float64

func (a mat) mul(b mat) mat {
	var r mat
	for c := range 3 {
		for w := range 3 {
			for k := range 3 {
				r[c*3+w] += a[k*3+w] * b[c*3+k]
			}
		}
	}
	return r
}

func (m mat) apply(v vec) vec {
	return vec{m[0]*v[0] + m[3]*v[1] + m[6]*v[2], m[1]*v[0] + m[4]*v[1] + m[7]*v[2], m[2]*v[0] + m[5]*v[1] + m[8]*v[2]}
}

// rotation is the camera at azimuth and elevation, times pitch, times roll.
func rotation(az, el, pitch, roll float64) mat {
	ca, sa, ce, se := math.Cos(az*rad), math.Sin(az*rad), math.Cos(el*rad), math.Sin(el*rad)
	cq, sq, cr, sr := math.Cos(pitch*rad), math.Sin(pitch*rad), math.Cos(roll*rad), math.Sin(roll*rad)
	return mat{ca, -sa * se, sa * ce, 0, ce, se, -sa, -ca * se, ca * ce}.mul(mat{cq, sq, 0, -sq, cq, 0, 0, 0, 1}.mul(mat{1, 0, 0, 0, cr, sr, 0, -sr, cr}))
}

// planeMesh: the last prop vertices turn with the propeller, the first paint
// triangles take the accent, and edges are the pairs of triangles that
// share one: a, b, then the triangles.
type planeMesh struct {
	v           []vec
	t           [][3]int
	e           [][4]int
	prop, paint int
	pivot       vec
}

var loadPlane = sync.OnceValue(func() *planeMesh {
	u16 := func(i int) int { return int(binary.LittleEndian.Uint16(planeBin[i*2:])) }
	f := func(i int) float64 { return float64(int16(u16(i))) / 4096 }
	nv, nt := u16(0), u16(2)
	m := &planeMesh{prop: u16(1), paint: u16(3), pivot: vec{f(4), f(5), f(6)}}
	for i := range nv {
		m.v = append(m.v, vec{f(7 + i*3), f(8 + i*3), f(9 + i*3)})
	}
	at, first := 7+nv*3, map[[2]int]int{}
	for k := range nt {
		t := [3]int{u16(at + k*3), u16(at + k*3 + 1), u16(at + k*3 + 2)}
		m.t = append(m.t, t)
		for j := range 3 {
			e := [2]int{min(t[j], t[(j+1)%3]), max(t[j], t[(j+1)%3])}
			if o, ok := first[e]; ok {
				m.e = append(m.e, [4]int{e[0], e[1], o, k})
			} else {
				first[e] = k
			}
		}
	}
	return m
})

// plane is the view. It draws over the screen into cells that hold no text:
// a run of up to 3 spaces between glyphs counts as text, so a line stays
// solid and the gaps in the wordmark open.
type plane struct {
	tel      *Node // text at the bottom: the plane fits in the rows above it; nil is every row
	on       func() bool
	t        float64 // seconds of animation so far, which stops while the terminal is blurred
	last     time.Time
	fly      time.Time // the key that flies it off; zero until then
	ex, ey   float64   // the eased mouse
	held     []bool    // the dots on in the last frame, at heldSize
	px       planePixels
	heldDens []float64
	heldSize [2]int
}

// flight is how far the bank, the turn and the run toward the camera are,
// from 0 to 1, and the camera's distance; done once the plane is gone.
func (p *plane) flight(now time.Time) (bank, turn, d, f float64, done bool) {
	if p.fly.IsZero() {
		return 0, 0, 0, math.Inf(1), false
	}
	u := now.Sub(p.fly).Seconds() / flyDur
	g := clamp01((u - flyGo) / (1 - flyGo))
	return smooth(u / flyBankT), smooth(u / flyTurnT), flyDist * g * g * g, flyLens / math.Pow(smooth(u/flyGo+1e-4), 2), u >= 1
}

// flown is whether the flight and the pause after it are over.
func (p *plane) flown(now time.Time) bool {
	return !p.fly.IsZero() && now.Sub(p.fly).Seconds() >= flyDur+flyAfter
}

func (p *plane) Draw(a *App, c *Canvas, r Rect) {
	now := time.Now()
	if p.last.IsZero() {
		p.last = now
	}
	dt := min(0.1, now.Sub(p.last).Seconds())
	p.last = now
	if a.Blurred {
		dt = 0
	}
	p.t += dt
	bank, turn, d, f, done := p.flight(now)
	area := r.H
	if p.tel != nil {
		area = p.tel.Rect.Y - r.Y
	}
	if !p.on() || done || area <= 0 {
		return
	}
	// The mouse from -1 to 1 about the center, held at the edge past
	// planeReach. The ease has a floor speed, so it arrives and stops.
	var mx, my float64
	if a.MouseX >= 0 {
		mx = clamp1((float64(a.MouseX) + .5 - float64(r.W)/2) / (float64(r.W) / 2) / planeReach)
		my = clamp1((float64(a.MouseY) + .5 - float64(r.H)/2) / (float64(r.H) / 2) / planeReach)
	}
	k := 1 - math.Exp(-dt/planeEase)
	ease := func(e *float64, to float64) {
		dd := to - *e
		*e += math.Copysign(min(math.Abs(dd), max(math.Abs(dd)*k, .5*dt)), dd)
	}
	ease(&p.ex, mx)
	ease(&p.ey, my)
	roll := flyBank * bank
	M := rotation(planeAzimuth+flyTurn*turn-p.ex*planeTilt, planeElevation+p.ey*planeTilt*.6, flyClimb*turn, roll)
	prop := (25 + p.t*planeProp*360*(1+(flyProp-1)*bank)) * rad
	cells := p.render(loadPlane(), M, roll, prop, d, f, r.W, r.H, area)
	inks := [2]term.Color{a.Theme.BG.Blend(a.Theme.Text, planeStrength), a.Theme.BG.Blend(a.Theme.Accent, planeStrength)}
	for y := range r.H {
		text := make([]bool, r.W)
		last := -9
		for x := range r.W {
			if cl := c.b.At(r.X+x, r.Y+y); cl.G != " " || cl.BG != a.Theme.BG {
				for i := max(0, last+1); x-last <= 4 && i <= x; i++ {
					text[i] = true
				}
				text[x], last = true, x
			}
		}
		for x := range r.W {
			if g := cells[y*r.W+x]; g.bits != 0 && !text[x] {
				c.Set(r.X+x, r.Y+y, string(rune(0x2800)+rune(g.bits)), 1, term.Style{FG: inks[g.ink]})
			}
		}
	}
}

// fit is pixels per model unit and the center. The box holds the plane at
// the base pose and at the four tilts a pointer in the window can give, so
// no tilt moves it out or zooms it. It fills a share of the width, more on
// small screens, and of the area's height, and leaves half a cell free on
// every side: braille draws in half cells.
func (m *planeMesh) fit(roll float64, W, cols, area int) (ppu, cx, cy float64) {
	t := planeTilt * min(1, 1/planeReach)
	lo, hi := vec{math.Inf(1), math.Inf(1)}, vec{math.Inf(-1), math.Inf(-1)}
	for _, s := range [][2]float64{{0, 0}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
		M := rotation(planeAzimuth+s[0]*t, planeElevation+s[1]*t*.6, 0, roll)
		for _, v := range m.v {
			q := M.apply(v)
			for k := range 2 {
				lo[k], hi[k] = min(lo[k], q[k]), max(hi[k], q[k])
			}
		}
	}
	bw, bh, w, h := hi[0]-lo[0], hi[1]-lo[1], float64(W), float64(area*planeSY)
	share := planeFillS + (planeFillL-planeFillS)*clamp01(float64(cols-60)/190)
	ppu = min(planeSize*min(share*w/bw, planeFillH*h/bh), (w-planeSX)/bw, (h-planeSY)/bh)
	return ppu, (hi[0] + lo[0]) / 2, (hi[1] + lo[1]) / 2
}

type planeCell struct{ bits, ink uint8 }

// planePixels are a frame's pixels, kept for the next frame: a depth, n.V,
// ink (-1 empty) and crease each.
type planePixels struct {
	depth, nv []float64
	ink       []int8
	crease    []bool
}

func (b *planePixels) clear(n int) {
	if cap(b.depth) < n {
		*b = planePixels{make([]float64, n), make([]float64, n), make([]int8, n), make([]bool, n)}
	}
	b.depth, b.nv, b.ink, b.crease = b.depth[:n], b.nv[:n], b.ink[:n], b.crease[:n]
	for i := range n {
		b.depth[i], b.ink[i], b.crease[i] = math.Inf(-1), -1, false
	}
}

var bayer4 = [16]float64{0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5}

// render is the braille cells of one pose: the plane d units along its nose
// and the eye f units away (Inf: orthographic), centered in the top area
// rows. The dots are the light, in the dark and the light theme alike.
func (p *plane) render(m *planeMesh, M mat, roll, prop, d, f float64, cols, rows, area int) []planeCell {
	W, H := cols*planeSX, rows*planeSY
	ppu, cx, cy := m.fit(roll, W, cols, area)
	mid := float64(area*planeSY) / 2
	p.px.clear(W * H)
	depth, nv, ink, crease := p.px.depth, p.px.nv, p.px.ink, p.px.crease
	pc, ps := math.Cos(prop), math.Sin(prop)
	Q, P := make([]vec, len(m.v)), make([]vec, len(m.v))
	zmin, zmax := math.Inf(1), math.Inf(-1)
	for i, v := range m.v {
		if i >= len(m.v)-m.prop {
			dy, dz := v[1]-m.pivot[1], v[2]-m.pivot[2]
			v = vec{v[0], m.pivot[1] + pc*dy - ps*dz, m.pivot[2] + ps*dy + pc*dz}
		}
		q := M.apply(vec{v[0] + d, v[1], v[2]})
		s := 1.0
		if !math.IsInf(f, 1) {
			s = f / (f - q[2])
		}
		Q[i], P[i] = q, vec{float64(W)/2 + (q[0]-cx)*ppu*s, mid - (q[1]-cy)*ppu*s, q[2]}
		zmin, zmax = min(zmin, q[2]), max(zmax, q[2])
	}
	near := func(i int) bool { return Q[i][2] > f-.5 } // past the eye
	N := make([]vec, len(m.t))
	for k, t := range m.t {
		N[k] = Q[t[1]].sub(Q[t[0]]).cross(Q[t[2]].sub(Q[t[0]])).norm()
		if N[k][2] <= 0 || near(t[0]) || near(t[1]) || near(t[2]) {
			continue
		}
		a, b, c := P[t[0]], P[t[1]], P[t[2]]
		ar := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
		if math.Abs(ar) < 1e-9 {
			continue
		}
		x0, x1 := max(0, int(math.Floor(min(a[0], b[0], c[0])))), min(W-1, int(math.Ceil(max(a[0], b[0], c[0]))))
		y0, y1 := max(0, int(math.Floor(min(a[1], b[1], c[1])))), min(H-1, int(math.Ceil(max(a[1], b[1], c[1]))))
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				px, py := float64(x)+.5, float64(y)+.5
				u := ((b[0]-px)*(c[1]-py) - (b[1]-py)*(c[0]-px)) / ar
				v := ((c[0]-px)*(a[1]-py) - (c[1]-py)*(a[0]-px)) / ar
				if u < 0 || v < 0 || u+v > 1 {
					continue
				}
				if z, i := u*a[2]+v*b[2]+(1-u-v)*c[2], y*W+x; z > depth[i] {
					depth[i], nv[i], ink[i] = z, N[k][2], 0
					if k < m.paint {
						ink[i] = 1
					}
				}
			}
		}
	}
	// Creases: edges between two visible faces at more than planeCrease,
	// depth tested.
	cos := math.Cos(planeCrease * rad)
	for _, e := range m.e {
		n1, n2 := N[e[2]], N[e[3]]
		if n1[2] <= 0 || n2[2] <= 0 || n1.dot(n2) >= cos || near(e[0]) || near(e[1]) {
			continue
		}
		a, b := P[e[0]], P[e[1]]
		dx, dy := b[0]-a[0], b[1]-a[1]
		steps := int(math.Ceil(math.Hypot(dx, dy)*2)) + 1
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			x, y := int(math.Floor(a[0]+dx*t)), int(math.Floor(a[1]+dy*t))
			if x >= 0 && y >= 0 && x < W && y < H && a[2]+(b[2]-a[2])*t >= depth[y*W+x]-.06 {
				crease[y*W+x] = true
			}
		}
	}

	// Dots: the rim light's tone of each 2x2 pixels. With hold, a dot needs
	// more pixels to appear than to stay.
	DW, DH, zr := cols*2, rows*4, zmax-zmin
	if zr == 0 {
		zr = 1
	}
	held := p.heldSize == [2]int{DW, DH}
	dens, depthN, dink, line := make([]float64, DW*DH), make([]float64, DW*DH), make([]uint8, DW*DH), make([]bool, DW*DH)
	for i := range DW * DH {
		dx, dy := i%DW, i/DW
		n, t, z, k, cr := 0, 0.0, 0.0, int8(0), false
		for y := dy * 2; y < dy*2+2; y++ {
			for x := dx * 2; x < dx*2+2; x++ {
				j := y*W + x
				cr = cr || crease[j]
				if ink[j] >= 0 {
					n, t, z, k = n+1, t+math.Pow(1-nv[j], planeRim), z+depth[j], ink[j]
				}
			}
		}
		need := 2
		if held {
			need = 1
			if p.heldDens[i] < 0 {
				need = 3
			}
		}
		if n < need {
			dens[i] = -1
			continue
		}
		tone := clamp01(planeAmbient + (1-planeAmbient)*t/float64(n))
		dens[i], depthN[i], dink[i], line[i] = planeDmax*tone, (z/float64(n)-zmin)/zr, uint8(k), cr
	}
	// Outlines: a dot beside an empty one, or beside one farther by planeDz.
	dz := planeDz / zr
	for i := range DW * DH {
		if dens[i] < 0 {
			continue
		}
		x := i % DW
		for _, o := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			j := i + o[1]*DW + o[0]
			if x+o[0] < 0 || x+o[0] >= DW || j < 0 || j >= DW*DH || dens[j] < 0 || depthN[i]-depthN[j] > dz {
				line[i] = true
			}
		}
	}
	on := make([]bool, DW*DH)
	for i := range on {
		s := 0.0
		if held {
			s = -planeHold
			if p.held[i] {
				s = planeHold
			}
		}
		on[i] = dens[i]+s > (bayer4[(i/DW%4)*4+i%DW%4]+.5)/16
	}
	p.held, p.heldDens, p.heldSize = on, dens, [2]int{DW, DH}

	out := make([]planeCell, cols*rows)
	bit := [4][2]uint8{{1, 8}, {2, 16}, {4, 32}, {64, 128}}
	for i := range out {
		row, col := i/cols, i%cols
		for j := range 4 {
			for x := range 2 {
				if k := (row*4+j)*DW + col*2 + x; dens[k] >= 0 {
					if on[k] || line[k] {
						out[i].bits |= bit[j][x]
					}
					out[i].ink = dink[k]
				}
			}
		}
	}
	return out
}

func clamp01(v float64) float64 { return max(0, min(1, v)) }
func clamp1(v float64) float64  { return max(-1, min(1, v)) }
func smooth(u float64) float64  { u = clamp01(u); return u * u * (3 - 2*u) }
