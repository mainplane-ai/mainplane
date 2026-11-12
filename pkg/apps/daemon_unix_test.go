//go:build !windows

package apps

import "syscall"

// gone is whether no process has the pid.
func gone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }
