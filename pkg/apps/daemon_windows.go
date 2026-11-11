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
