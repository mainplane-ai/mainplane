package worker

import (
	"os"
	"os/exec"
	"strconv"
)

// Windows has no process groups to signal; taskkill walks the tree.
func group(*exec.Cmd) {}

func killTree(p *os.Process) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
}
