package apps

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A process of the tree tests ends by itself after this, should a test fail
// to end it.
const treeLife = 5 * time.Minute

// treeDaemon is a daemon app of the tree tests, this binary run with a mode:
// "stays" and "leaves" start a child, say its pid in $DATA_DIR/child, and
// then stay or exit; the "child" only stays.
func treeDaemon(mode string) {
	if mode != "child" {
		c := exec.Command(os.Args[0], "child")
		if err := c.Start(); err != nil {
			panic(err)
		}
		f, err := os.OpenFile(filepath.Join(os.Getenv("DATA_DIR"), "child"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			panic(err)
		}
		_, _ = fmt.Fprintln(f, c.Process.Pid)
		_ = f.Close()
	}
	if mode != "leaves" {
		time.Sleep(treeLife)
	}
}

// A daemon's whole process tree ends with it: when an admin stops it, even
// after its app.json changed, and when its first process exits and leaves
// others behind.
func TestDaemonTree(t *testing.T) {
	r := newRig(t)
	r.install("stays", Spec{Run: []string{os.Args[0], "stays"}})
	r.install("leaves", Spec{Run: []string{os.Args[0], "leaves"}})
	// child is the first process the app left in the background, once it says so.
	child := func(app string) int {
		pid := 0
		r.until(func() bool {
			b, _ := os.ReadFile(filepath.Join(r.dir, "app-data", app, "child"))
			line, _, whole := strings.Cut(string(b), "\n")
			pid, _ = strconv.Atoi(strings.TrimSpace(line))
			return whole && pid > 0
		})
		return pid
	}
	r.want(204, "full", "POST", "/apps/stays/start", "")
	r.want(204, "full", "POST", "/apps/leaves/start", "")
	stays, leaves := child("stays"), child("leaves")
	r.until(func() bool { return gone(leaves) }) // its first process has exited: what it left goes too
	if gone(stays) {
		t.Fatal("the running daemon's child is gone")
	}
	// a daemon stops though its app.json has become one the harness refuses
	r.install("stays", Spec{Run: []string{os.Args[0]}, Secrets: []string{"PATH"}})
	r.want(400, "full", "POST", "/apps/stays/start", "")
	r.want(204, "full", "POST", "/apps/stays/stop", "")
	r.want(204, "full", "POST", "/apps/leaves/stop", "")
	r.until(func() bool { return gone(stays) })
}
