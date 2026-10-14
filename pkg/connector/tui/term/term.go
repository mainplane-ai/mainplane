// Package term owns the terminal for a full screen app: raw mode and the
// alternate screen with a guaranteed restore, capability queries, input
// decoding, a cell buffer and a diff renderer.
package term

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

// queryWait bounds the startup capability queries. Every terminal answers DA1,
// which ends the wait at once; this is for those that answer nothing, and
// for ssh, where a round trip can take a few hundred ms.
const queryWait = 500 * time.Millisecond

const (
	enter = "\x1b[?1049h" + // alternate screen
		"\x1b[22;0t" + // push the title, popped on exit
		"\x1b[?25l\x1b[?7l" + // hide the cursor, no autowrap: the last column is ours
		"\x1b[?1000h\x1b[?1002h\x1b[?1006h" + // mouse press, drag, SGR coordinates
		"\x1b[?2004h\x1b[?1004h" // bracketed paste, focus events
	query = "\x1b[?u" + // kitty keyboard flags
		"\x1b]11;?\x1b\\" + // background color
		"\x1b[c" // DA1, answered by all: the end of the replies
	leave = "\x1b]9;4;0\x07" + // no progress
		"\x1b[?1004l\x1b[?2004l" +
		"\x1b[?1006l\x1b[?1002l\x1b[?1000l" +
		"\x1b[0m\x1b[?7h\x1b[?25h" +
		"\x1b[?1049l" +
		"\x1b[23;0t"
	kittyPush = "\x1b[>1u" // disambiguate: Shift+Enter, Ctrl+I and Esc are their own keys
	kittyPop  = "\x1b[<u"
)

// Caps is what the terminal said it can do.
type Caps struct {
	Truecolor bool
	Dark      bool
	Kitty     bool
}

// Terminal is the open terminal. Events carries input, focus and resize. Every
// write goes through Write, which holds the lock Close takes, so a restore
// from a signal never interleaves with a frame.
type Terminal struct {
	Events chan Event
	Caps   Caps

	in, out *os.File
	raw     *term.State
	plat    func() // undoes the platform console modes
	mu      sync.Mutex
	w       *bufio.Writer
	closed  bool
	stop    chan struct{}

	replies chan Event // replies to the startup queries, in order
}

// Open takes the terminal: raw mode, alternate screen, mouse, paste and focus
// reporting, then asks what the terminal can do. A signal restores it and
// exits.
func Open() (*Terminal, error) {
	in, out := os.Stdin, os.Stdout
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return nil, errors.New("not a terminal")
	}
	t := &Terminal{Events: make(chan Event, 256), in: in, out: out, w: bufio.NewWriterSize(out, 64<<10), stop: make(chan struct{}), replies: make(chan Event, 8)}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s := <-sig
		t.Close()
		fmt.Fprintln(os.Stderr, s)
		os.Exit(1)
	}()
	if err := t.modes(); err != nil {
		t.Close()
		return nil, err
	}
	go t.read()
	go t.resizes()
	t.Write(enter + query)
	t.Flush()
	t.Caps = Caps{Truecolor: truecolor(), Dark: true}
	deadline := time.After(queryWait)
	for done := false; !done; {
		select {
		case r := <-t.replies:
			switch r := r.(type) {
			case kittyReply:
				t.Caps.Kitty = true
			case bgReply:
				t.Caps.Dark = r.luma() < 0.5
			case da1Reply:
				done = true
			}
		case <-deadline:
			done = true
		}
	}
	if t.Caps.Kitty {
		t.Write(kittyPush)
		t.Flush()
	}
	return t, nil
}

// modes sets raw mode and the platform's console modes, under the lock Close
// takes, so a signal in between restores what was set.
func (t *Terminal) modes() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var err error
	if t.raw, err = term.MakeRaw(int(t.in.Fd())); err != nil {
		return err
	}
	t.plat, err = setup(t.in, t.out)
	return err
}

// truecolor is whether the terminal takes 24-bit color. There is no reliable
// query, so it is the convention: COLORTERM, or a terminal known to have it.
// Terminal.app sets neither, and gets 256 colors.
func truecolor() bool {
	ct := os.Getenv("COLORTERM")
	return ct == "truecolor" || ct == "24bit" || os.Getenv("WT_SESSION") != "" || isWindows
}

// Size is the terminal's width and height in cells.
func (t *Terminal) Size() (int, int) {
	w, h, err := term.GetSize(int(t.out.Fd()))
	if err != nil {
		return 80, 24
	}
	return w, h
}

// Write queues bytes for the terminal; Flush sends them.
func (t *Terminal) Write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		_, _ = t.w.WriteString(s)
	}
}

func (t *Terminal) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		_ = t.w.Flush()
	}
}

// Close gives the terminal back as it was. It is safe to call twice and from
// any goroutine.
func (t *Terminal) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	close(t.stop)
	if t.Caps.Kitty {
		_, _ = t.w.WriteString(kittyPop)
	}
	_, _ = t.w.WriteString(leave)
	_ = t.w.Flush()
	if t.plat != nil {
		t.plat()
	}
	if t.raw != nil {
		_ = term.Restore(int(t.in.Fd()), t.raw)
	}
}

// Recover is deferred at the top of every goroutine that may panic while the
// terminal is open: the terminal is restored before the panic prints.
func (t *Terminal) Recover() {
	if r := recover(); r != nil {
		t.Close()
		panic(r)
	}
}

// read decodes stdin into events. It waits for input in short slices so it
// stops reading once the terminal is closed, and a program run after the TUI
// gets the keys.
func (t *Terminal) read() {
	defer t.Recover()
	var d decoder
	buf := make([]byte, 4096)
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		ready, err := wait(t.in, d.wait())
		if err != nil {
			return
		}
		if !ready {
			d.timeout(t.emit)
			continue
		}
		n, err := t.in.Read(buf)
		if err != nil {
			return
		}
		d.feed(buf[:n], t.emit)
	}
}

func (t *Terminal) emit(e Event) {
	switch e.(type) {
	case kittyReply, bgReply, da1Reply:
		select {
		case t.replies <- e:
		default:
		}
	default:
		t.Events <- e
	}
}

// SetTitle sets the window or tab title.
func (t *Terminal) SetTitle(s string) {
	t.Write("\x1b]2;" + strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s) + "\x07")
}

// Progress shows the busy indicator in the tab and taskbar (OSC 9;4), or
// clears it.
func (t *Terminal) Progress(busy bool) {
	if busy {
		t.Write("\x1b]9;4;3\x07")
	} else {
		t.Write("\x1b]9;4;0\x07")
	}
}
