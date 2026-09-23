//go:build !windows

// Package elevate makes a command that needs root or admin work without the
// user typing sudo or opening an elevated shell first.
package elevate

import (
	"log"
	"os"
	"os/exec"
	"syscall"
)

// Root returns when this process is root. Otherwise it becomes sudo running
// this binary with args, so the password is asked in this terminal and the
// exit is sudo's.
func Root(args ...string) {
	if os.Geteuid() == 0 {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		log.Fatal("this needs root, and there is no sudo: ", err)
	}
	log.Fatal(syscall.Exec(sudo, append([]string{"sudo", exe}, args...), os.Environ()))
}
