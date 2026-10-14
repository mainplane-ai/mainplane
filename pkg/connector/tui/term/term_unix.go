//go:build !windows

package term

import (
	"errors"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
)

const isWindows = false

func setup(in, out *os.File) (func(), error) { return func() {}, nil }

// wait is whether stdin has bytes to read now.
func wait(f *os.File, timeout time.Duration) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout/time.Millisecond))
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return n > 0, err
}

func (t *Terminal) resizes() {
	defer t.Recover()
	c := make(chan os.Signal, 1)
	signal.Notify(c, unix.SIGWINCH)
	defer signal.Stop(c)
	for {
		select {
		case <-c:
			w, h := t.Size()
			t.Events <- Resize{W: w, H: h}
		case <-t.stop:
			return
		}
	}
}
