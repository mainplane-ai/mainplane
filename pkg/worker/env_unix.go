//go:build !windows

package worker

import (
	"os"
	"os/exec"
	"syscall"
)

// group puts the interpreter in its own process group so killTree reaches
// every process it started.
func group(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killTree(p *os.Process) { _ = syscall.Kill(-p.Pid, syscall.SIGKILL) }
