//go:build windows

package apps

import (
	"os"
	"os/exec"
	"strconv"
)

func setProcGroup(cmd *exec.Cmd) {}

func killProcGroup(p *os.Process) {
	if p != nil && p.Pid > 0 {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
		_ = p.Kill()
	}
}
