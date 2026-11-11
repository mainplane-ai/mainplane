//go:build !windows

package apps

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// A daemon's whole process tree ends with it: when an admin stops it, and
// when its first process exits and leaves others behind.
func TestDaemonTree(t *testing.T) {
	r := newRig(t)
	r.install("stays", Spec{Run: []string{"sh", "-c", "sleep 300 & echo $! > $DATA_DIR/child; exec sleep 300"}})
	r.install("leaves", Spec{Run: []string{"sh", "-c", "sleep 300 & echo $! >> $DATA_DIR/child"}})
	// child is the first process the app left in the background, once it says so.
	child := func(app string) int {
		pid := 0
		r.until(func() bool {
			b, _ := os.ReadFile(filepath.Join(r.dir, "app-data", app, "child"))
			line, _, whole := strings.Cut(string(b), "\n")
			pid, _ = strconv.Atoi(line)
			return whole && pid > 0
		})
		return pid
	}
	gone := func(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }
	r.want(204, "full", "POST", "/apps/stays/start", "")
	r.want(204, "full", "POST", "/apps/leaves/start", "")
	stays, leaves := child("stays"), child("leaves")
	r.until(func() bool { return gone(leaves) }) // its sh has exited: what it left goes too
	if gone(stays) {
		t.Fatal("the running daemon's child is gone")
	}
	r.want(204, "full", "POST", "/apps/stays/stop", "")
	r.want(204, "full", "POST", "/apps/leaves/stop", "")
	r.until(func() bool { return gone(stays) })
}
