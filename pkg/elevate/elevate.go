// Package elevate makes a command that needs root or admin work without the
// user typing sudo or opening an elevated shell first.
package elevate

import "os"

// Root returns when this process is root or admin. Otherwise it runs this
// binary with args elevated and exits as that does.
func Root(args ...string) {
	if !Is() {
		os.Exit(Run(args...))
	}
}
