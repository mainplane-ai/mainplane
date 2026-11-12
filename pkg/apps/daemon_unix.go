//go:build !windows

package apps

import (
	"os/exec"
	"syscall"
)

// inherit is what a daemon takes of the harness's environment, where it is
// set: where programs are, the user's home, where temporary files go, and
// the locale. Nothing else: a provider key may be there.
var inherit = []string{"PATH", "HOME", "TMPDIR", "LANG"}

// tree is a daemon's process group: its first process and all that one
// started.
type tree struct{ cmd *exec.Cmd }

// start runs cmd as the leader of a process group of its own, which the end
// of cmd's context kills whole.
func start(cmd *exec.Cmd) (tree, error) {
	t := tree{cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = t.kill
	return t, cmd.Start()
}

func (t tree) kill() error { return syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL) }

// sweep kills what an exited daemon left behind: the group outlives its
// first process, and an empty one answers ESRCH.
func (t tree) sweep() { _ = t.kill() }
