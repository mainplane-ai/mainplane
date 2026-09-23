// Package elevate makes a command that needs root or admin work without the
// user typing sudo or opening an elevated shell first.
package elevate

import (
	"errors"
	"log"
	"os"
	"os/exec"
)

// Root returns when this process is root or admin. Otherwise it runs this
// binary with args elevated and exits as that does.
func Root(args ...string) {
	if !Is() {
		os.Exit(Run(args...))
	}
}

// code is how a finished child exited; a child that never ran is fatal.
func code(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		log.Fatal(err)
	}
	return 0
}
