//go:build !linux && !windows

package worker

import (
	"os/user"
	"runtime"
)

// reconcile says what this OS cannot do yet: only Linux serves drives, and
// clients on other OSes come later.
func reconcile(want Desired, _ *user.User) []Drive {
	var have []Drive
	for _, e := range want.Serve {
		have = append(have, Drive{Name: e.Name, Serve: true, State: Failed, Error: "only a Linux worker serves drives"})
	}
	for _, m := range want.Mount {
		have = append(have, Drive{Name: m.Name, State: Failed, Error: "a " + runtime.GOOS + " worker does not mount drives yet"})
	}
	return have
}

func dropDrives(*user.User) error { return nil }
