package term

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const isWindows = true

// keysOn turns on win32-input-mode: every key comes as a record with its
// modifiers, so Shift+Enter is not a plain CR.
const keysOn, keysOff = "\x1b[?9001h", "\x1b[?9001l"

// resizePoll is how often the size is checked. A console in VT input mode
// does not report resizes through ReadFile.
const resizePoll = 100 * time.Millisecond

const cpUTF8 = 65001

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	peekConsoleInput = kernel32.NewProc("PeekConsoleInputW")
	readConsoleInput = kernel32.NewProc("ReadConsoleInputW")
)

// save returns what puts the console modes and code pages back as they are
// now.
func save(in, out *os.File) (func(), error) {
	hin, hout := windows.Handle(in.Fd()), windows.Handle(out.Fd())
	var inMode, outMode uint32
	if err := windows.GetConsoleMode(hin, &inMode); err != nil {
		return nil, err
	}
	if err := windows.GetConsoleMode(hout, &outMode); err != nil {
		return nil, err
	}
	icp, _ := windows.GetConsoleCP()
	ocp, _ := windows.GetConsoleOutputCP()
	return func() {
		_ = windows.SetConsoleMode(hin, inMode)
		_ = windows.SetConsoleMode(hout, outMode)
		_ = windows.SetConsoleCP(icp)
		_ = windows.SetConsoleOutputCP(ocp)
	}, nil
}

// setup makes the console speak VT both ways in UTF-8: keys, mouse and paste
// arrive as escape sequences, and quick edit no longer takes the mouse.
func setup(in, out *os.File) (func(), error) {
	hin, hout := windows.Handle(in.Fd()), windows.Handle(out.Fd())
	restore, err := save(in, out)
	if err != nil {
		return nil, err
	}
	var inMode, outMode uint32
	_ = windows.GetConsoleMode(hin, &inMode)
	_ = windows.GetConsoleMode(hout, &outMode)
	err = windows.SetConsoleMode(hin, (inMode|windows.ENABLE_EXTENDED_FLAGS|windows.ENABLE_VIRTUAL_TERMINAL_INPUT)&^(windows.ENABLE_QUICK_EDIT_MODE|windows.ENABLE_MOUSE_INPUT|windows.ENABLE_WINDOW_INPUT))
	if err == nil {
		err = windows.SetConsoleMode(hout, outMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN|windows.ENABLE_PROCESSED_OUTPUT)
	}
	if err == nil {
		err = windows.SetConsoleCP(cpUTF8)
	}
	if err == nil {
		err = windows.SetConsoleOutputCP(cpUTF8)
	}
	if err != nil {
		restore()
		return nil, err
	}
	return restore, nil
}

// inputRecord is the console's INPUT_RECORD with the KEY_EVENT_RECORD arm.
type inputRecord struct {
	Type    uint16
	_       uint16
	KeyDown int32
	Repeat  uint16
	VK      uint16
	Scan    uint16
	Char    uint16
	Ctrl    uint32
}

// modifier is the virtual keys that type nothing alone: shift, ctrl, alt,
// caps, num and scroll lock, the Windows keys, and their left and right forms.
var modifier = map[uint16]bool{0x10: true, 0x11: true, 0x12: true, 0x14: true, 0x5b: true, 0x5c: true, 0xa0: true, 0xa1: true, 0xa2: true, 0xa3: true, 0xa4: true, 0xa5: true, 0x90: true, 0x91: true}

// wait is whether ReadFile will return bytes now. The console's handle is
// also signaled by records ReadFile skips (key ups, focus, menu); those are
// dropped here, or ReadFile would block past the terminal's close.
func wait(f *os.File, timeout time.Duration) (bool, error) {
	h := windows.Handle(f.Fd())
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		return false, err
	}
	var recs [64]inputRecord
	var n uint32
	if r, _, err := peekConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&n))); r == 0 {
		return false, err
	}
	for _, r := range recs[:n] {
		if r.Type == 1 && r.KeyDown != 0 && !modifier[r.VK] { // KEY_EVENT; arrows have no char until ReadFile makes them VT
			return true, nil
		}
	}
	if r, _, err := readConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(n), uintptr(unsafe.Pointer(&n))); r == 0 {
		return false, err
	}
	return false, nil
}

func (t *Terminal) resizes() {
	defer t.Recover()
	w, h := t.Size()
	tick := time.NewTicker(resizePoll)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if nw, nh := t.Size(); nw != w || nh != h {
				w, h = nw, nh
				t.send(Resize{W: w, H: h})
			}
		case <-t.stop:
			return
		}
	}
}
