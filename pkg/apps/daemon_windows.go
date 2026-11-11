package apps

import (
	"os"
	"os/exec"
	"strconv"
)

func own(*exec.Cmd) {}

// Windows has no process groups to signal; taskkill walks the tree.
func killTree(p *os.Process) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
}

// sweep leaves what an exited daemon left behind: taskkill finds a tree by
// its root, and the root's pid may be another process's by now.
func sweep(*os.Process) {}
