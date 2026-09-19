package worker

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// JoinPath is where install leaves the join token and the service reads it:
// the operator's own file, so neither the service definition nor the command
// line, both readable by every local user, carries the secret.
func JoinPath(home string) string { return filepath.Join(home, ".mainplane", "join") }

// run is one service manager command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
