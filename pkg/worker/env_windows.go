package worker

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
)

// prepare gives cmd the worker's environment. The Windows worker runs as its
// operator, so op is always nil.
func prepare(cmd *exec.Cmd, _ *user.User) error {
	cmd.Env = os.Environ()
	return nil
}

// Windows has no process groups to signal; taskkill walks the tree.
func killTree(p *os.Process) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
}
