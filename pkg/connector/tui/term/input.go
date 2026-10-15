package term

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// escWait is how long a lone ESC, or the start of a sequence, waits for
	// the rest before it is the Esc key. Sequences arrive in one write, so
	// this only has to cover a slow link.
	escWait = 50 * time.Millisecond
	// idleWait is how often an idle reader checks that the terminal is open.
	idleWait = 100 * time.Millisecond
)

// Event is Key, Mouse, Paste, Focus or Resize.
type Event any

type Mod uint8

const (
	Shift Mod = 1 << iota
	Alt
	Ctrl
)

// Keys that are not text have negative codes.
const (
	KeyEnter rune = -1 - iota
	KeyTab
	KeyBackspace
	KeyEsc
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyInsert
	KeyDelete
	KeyF1
)

var keyNames = map[rune]string{
	KeyEnter: "enter", KeyTab: "tab", KeyBackspace: "backspace", KeyEsc: "esc", KeyUp: "up", KeyDown: "down",
	KeyLeft: "left", KeyRight: "right", KeyHome: "home", KeyEnd: "end", KeyPgUp: "pgup", KeyPgDn: "pgdn",
	KeyInsert: "insert", KeyDelete: "delete", KeyF1: "f1", ' ': "space",
}

// Key is one key press: a rune of text or a Key constant, and its modifiers.
// Ctrl and Alt letters are lower case.
type Key struct {
	Code rune
	Mod  Mod
}

// String is the key as a keymap writes it: "ctrl+c", "shift+enter", "a".
func (k Key) String() string {
	var b strings.Builder
	for _, m := range []struct {
		m Mod
		s string
	}{{Ctrl, "ctrl+"}, {Alt, "alt+"}, {Shift, "shift+"}} {
		if k.Mod&m.m != 0 {
			b.WriteString(m.s)
		}
	}
	if n, ok := keyNames[k.Code]; ok {
		b.WriteString(n)
	} else {
		b.WriteRune(k.Code)
	}
	return b.String()
}

// Text is the text the key types, or "" when it types none.
func (k Key) Text() string {
	if k.Code < ' ' || k.Mod&(Ctrl|Alt) != 0 {
		return ""
	}
	return string(k.Code)
}

type Button uint8

const (
	Left Button = iota
	Middle
	Right
	NoButton
	WheelUp
	WheelDown
)

type Action uint8

const (
	Press Action = iota
	Release
	Motion
)

// Mouse is a mouse event at cell X, Y, counted from 0.
type Mouse struct {
	X, Y   int
	Button Button
	Action Action
	Mod    Mod
}

type Paste struct{ Text string }

type Focus struct{ In bool }

type Resize struct{ W, H int }

// Replies to the startup queries.
type (
	kittyReply struct{}
	da1Reply   struct{}
	bgReply    struct{ r, g, b float64 }
	pasteStart struct{}
)

func (c bgReply) luma() float64 { return 0.2126*c.r + 0.7152*c.g + 0.0722*c.b }

// decoder turns stdin bytes into events. It keeps bytes that may be the
// start of a sequence until the rest arrives or escWait passes.
type decoder struct {
	buf     []byte
	pasting bool
	paste   []byte
}

func (d *decoder) wait() time.Duration {
	if len(d.buf) > 0 {
		return escWait
	}
	return idleWait
}

func (d *decoder) feed(b []byte, emit func(Event)) {
	d.buf = append(d.buf, b...)
	d.parse(emit, false)
}

// timeout is called when no byte came within wait: what is held is complete.
func (d *decoder) timeout(emit func(Event)) {
	if len(d.buf) > 0 && !d.pasting {
		d.parse(emit, true)
	}
}

const pasteEnd = "\x1b[201~"

func (d *decoder) parse(emit func(Event), force bool) {
	for len(d.buf) > 0 {
		if d.pasting {
			i := bytes.Index(d.buf, []byte(pasteEnd))
			if i < 0 {
				keep := max(0, len(d.buf)-len(pasteEnd)+1) // a partial end marker stays
				d.paste = append(d.paste, d.buf[:keep]...)
				d.buf = d.buf[keep:]
				return
			}
			d.paste = append(d.paste, d.buf[:i]...)
			emit(Paste{Text: strings.ReplaceAll(strings.ReplaceAll(string(d.paste), "\r\n", "\n"), "\r", "\n")})
			d.buf, d.paste, d.pasting = d.buf[i+len(pasteEnd):], nil, false
			continue
		}
		n, ev := parseOne(d.buf)
		if n == 0 {
			if !force {
				return
			}
			n, ev = 1, nil // a lone ESC is the Esc key; a broken rune is dropped
			if d.buf[0] == 0x1b {
				ev = Key{Code: KeyEsc}
			}
		}
		d.buf = d.buf[n:]
		switch ev.(type) {
		case nil:
		case pasteStart:
			d.pasting = true
		default:
			emit(ev)
		}
	}
	d.buf = nil
}

// parseOne decodes the event at the start of b and its length; 0 means b
// may be the start of something longer.
func parseOne(b []byte) (int, Event) {
	if b[0] != 0x1b {
		return char(b, 0)
	}
	if len(b) == 1 {
		return 0, nil
	}
	switch b[1] {
	case '[':
		for i := 2; i < len(b); i++ {
			switch c := b[i]; {
			case c >= 0x40 && c <= 0x7e:
				return i + 1, csi(string(b[2:i]), c)
			case c < 0x20:
				return 2, Key{Code: '[', Mod: Alt}
			}
		}
		return 0, nil
	case ']', 'P', '_', '^': // OSC, DCS, APC, PM: to BEL or ST
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1, osc(string(b[2:i]))
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				if b[1] == ']' {
					return i + 2, osc(string(b[2:i]))
				}
				return i + 2, nil
			}
		}
		return 0, nil
	case 'O':
		if len(b) < 3 {
			return 0, nil
		}
		if k, ok := final[b[2]]; ok {
			return 3, Key{Code: k}
		}
		return 3, nil
	case 0x1b:
		return 1, Key{Code: KeyEsc}
	}
	n, ev := char(b, 1)
	if k, ok := ev.(Key); ok {
		k.Mod |= Alt
		ev = k
	}
	return n, ev
}

// char decodes one control byte or rune at b[i:].
func char(b []byte, i int) (int, Event) {
	c := b[i]
	switch {
	case c == '\r':
		return i + 1, Key{Code: KeyEnter}
	case c == '\t':
		return i + 1, Key{Code: KeyTab}
	case c == 0x7f:
		return i + 1, Key{Code: KeyBackspace}
	case c == 0x08:
		return i + 1, Key{Code: KeyBackspace, Mod: Ctrl}
	case c == 0:
		return i + 1, Key{Code: ' ', Mod: Ctrl}
	case c < 0x1b:
		return i + 1, Key{Code: rune('a' + c - 1), Mod: Ctrl}
	case c < 0x20:
		return i + 1, Key{Code: rune('\\' + c - 0x1c), Mod: Ctrl}
	}
	if !utf8.FullRune(b[i:]) {
		return 0, nil
	}
	r, n := utf8.DecodeRune(b[i:])
	return i + n, Key{Code: r}
}

// final maps the last byte of CSI and SS3 cursor keys.
var final = map[byte]rune{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd, 'P': KeyF1}

// tilde maps the number of a CSI ... ~ key.
var tilde = map[int]rune{1: KeyHome, 2: KeyInsert, 3: KeyDelete, 4: KeyEnd, 5: KeyPgUp, 6: KeyPgDn, 7: KeyHome, 8: KeyEnd, 11: KeyF1}

// kittyCode maps the code of a kitty CSI ... u key that is not text.
var kittyCode = map[int]rune{13: KeyEnter, 9: KeyTab, 27: KeyEsc, 127: KeyBackspace}

func csi(p string, f byte) Event {
	prefix := byte(0)
	if p != "" && p[0] >= '<' && p[0] <= '?' {
		prefix, p = p[0], p[1:]
	}
	n := nums(p)
	arg := func(i, def int) int {
		if i < len(n) && n[i] > 0 {
			return n[i]
		}
		return def
	}
	mod := Mod(arg(1, 1)-1) & (Shift | Alt | Ctrl)
	switch {
	case prefix == '<' && (f == 'M' || f == 'm'):
		return mouse(arg(0, 0), arg(1, 1)-1, arg(2, 1)-1, f == 'm')
	case prefix == '?' && f == 'u':
		return kittyReply{}
	case prefix == '?' && f == 'c':
		return da1Reply{}
	case prefix != 0:
		return nil
	case f == 'I' || f == 'O':
		return Focus{In: f == 'I'}
	case f == 'Z':
		return Key{Code: KeyTab, Mod: Shift}
	case f == 'u':
		code := arg(0, 0)
		if k, ok := kittyCode[code]; ok {
			return Key{Code: k, Mod: mod}
		}
		if mod&(Ctrl|Alt) != 0 && code >= 'A' && code <= 'Z' {
			code += 'a' - 'A'
		}
		return Key{Code: rune(code), Mod: mod}
	case f == '~':
		switch arg(0, 0) {
		case 200:
			return pasteStart{}
		case 27: // xterm modifyOtherKeys: 27;mod;code~
			if k, ok := kittyCode[arg(2, 0)]; ok {
				return Key{Code: k, Mod: mod}
			}
			return Key{Code: rune(arg(2, 0)), Mod: mod}
		}
		if k, ok := tilde[arg(0, 0)]; ok {
			return Key{Code: k, Mod: mod}
		}
	}
	if k, ok := final[f]; ok {
		return Key{Code: k, Mod: mod}
	}
	return nil
}

// nums is the numbers of a parameter string; a sub-parameter after ':' is
// dropped.
func nums(p string) []int {
	var n []int
	for _, s := range strings.Split(p, ";") {
		s, _, _ = strings.Cut(s, ":")
		v, _ := strconv.Atoi(s)
		n = append(n, v)
	}
	return n
}

func mouse(b, x, y int, release bool) Event {
	m := Mouse{X: x, Y: y, Button: Button(b & 3), Action: Press}
	if b&4 != 0 {
		m.Mod |= Shift
	}
	if b&8 != 0 {
		m.Mod |= Alt
	}
	if b&16 != 0 {
		m.Mod |= Ctrl
	}
	switch {
	case b&64 != 0:
		if b&3 > 1 {
			return nil // horizontal wheel
		}
		m.Button = WheelUp + Button(b&1)
	case b&32 != 0:
		m.Action = Motion
	case release:
		m.Action = Release
	}
	return m
}

// osc decodes the background color reply, 11;rgb:RRRR/GGGG/BBBB, with one to
// four hex digits per channel.
func osc(s string) Event {
	rest, ok := strings.CutPrefix(s, "11;rgb:")
	if !ok {
		return nil
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return nil
	}
	var c [3]float64
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 16)
		if err != nil || len(p) == 0 || len(p) > 4 {
			return nil
		}
		c[i] = float64(v) / float64(uint64(1)<<(4*len(p))-1)
	}
	return bgReply{c[0], c[1], c[2]}
}
