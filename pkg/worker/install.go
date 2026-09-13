package worker

import (
	"fmt"
	"os/exec"
	"strings"
)

// run is one service manager command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
