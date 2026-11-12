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
// "stays" and "leaves" start a child, say its pid and their own in
// $DATA_DIR/child and $DATA_DIR/root, and then stay or exit; the "child"
// only stays.
func treeDaemon(mode string) {
	if mode != "child" {
		c := exec.Command(os.Args[0], "child")
		if err := c.Start(); err != nil {
			panic(err)
		}
		for name, pid := range map[string]int{"child": c.Process.Pid, "root": os.Getpid()} {
			f, err := os.OpenFile(filepath.Join(os.Getenv("DATA_DIR"), name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				panic(err)
			}
			_, _ = fmt.Fprintln(f, pid)
			_ = f.Close()
		}
	}
	if mode != "leaves" {
		time.Sleep(treeLife)
	}
}

// said is the pid a tree daemon's first launch wrote in the file, once it
// has: "child" the process it left in the background, "root" its own.
func (r *rig) said(app, file string) int {
	r.t.Helper()
	pid := 0
	r.until(func() bool {
		b, _ := os.ReadFile(filepath.Join(r.dir, "app-data", app, file))
		line, _, whole := strings.Cut(string(b), "\n")
		pid, _ = strconv.Atoi(strings.TrimSpace(line))
		return whole && pid > 0
	})
	return pid
}

// A daemon's whole process tree ends with it: when an admin stops it, even
// after its app.json changed, and when its first process exits and leaves
// others behind.
func TestDaemonTree(t *testing.T) {
	r := newRig(t)
	r.install("stays", Spec{Run: []string{os.Args[0], "stays"}})
	r.install("leaves", Spec{Run: []string{os.Args[0], "leaves"}})
	r.want(204, "full", "POST", "/apps/stays/start", "")
	r.want(204, "full", "POST", "/apps/leaves/start", "")
	stays, leaves := r.said("stays", "child"), r.said("leaves", "child")
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

// Close kills the daemons and returns only once they are dead: the harness
// exits after it, and leaves none. None starts after it.
func TestDaemonsClosed(t *testing.T) {
	r := newRig(t)
	r.install("stays", Spec{Run: []string{os.Args[0], "stays"}})
	r.want(204, "full", "POST", "/apps/stays/start", "")
	root, child := r.said("stays", "root"), r.said("stays", "child")
	if gone(root) || gone(child) {
		t.Fatal("the daemon is not running")
	}
	r.m.Close()
	if !gone(root) {
		t.Fatal("Close returned and the daemon's first process is there")
	}
	r.until(func() bool { return gone(child) }) // killed with it, reaped by the system in its own time
	r.want(204, "full", "POST", "/apps/stays/start", "")
	if got := r.want(200, "full", "GET", "/apps", ""); !strings.Contains(got, `"status":"stopped"`) {
		t.Fatalf("a daemon after Close: %s", got)
	}
}
