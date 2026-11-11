//go:build !windows

package apps

import (
	"os"
	"os/exec"
	"syscall"
)

// own puts cmd in a process group of its own, so killTree reaches every
// process it started.
func own(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killTree(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGKILL) }

// sweep kills what an exited daemon left behind: the group outlives its
// first process, and an empty one answers ESRCH.
func sweep(p *os.Process) { _ = killTree(p) }
