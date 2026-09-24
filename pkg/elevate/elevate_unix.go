//go:build !windows

package elevate

import (
	"errors"
	"log"
	"os"
	"os/exec"
)

// Is says whether this process is root.
func Is() bool { return os.Geteuid() == 0 }

// Run runs this binary with args under sudo, which asks for the password in
// this terminal, and returns its exit code. A sudo that never ran is fatal.
func Run(args ...string) int {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command("sudo", append([]string{exe}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		log.Fatal(err)
	}
	return 0
}
