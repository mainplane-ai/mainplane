package server

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/release"
)

// Update makes the installed harness release v, the newest stable release
// when v is empty, prints what the changelog says changed, and restarts it.
// Workers follow at their next hello, so one command updates the fleet.
func Update(v string) error {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return fmt.Errorf("no installed harness at %s: %w", bin, err)
	}
	cur := string(bytes.TrimSpace(out))
	if v == "" {
		if v, err = release.Latest(); err != nil {
			return err
		}
	}
	if v == cur {
		fmt.Printf("mainplane-server is %s\n", v)
		return nil
	}
	log, err := release.Get(v, "CHANGELOG.md")
	if err != nil {
		return err
	}
	if err := release.Install(bin, "mainplane-server", v); err != nil {
		return err
	}
	fmt.Printf("mainplane-server %s -> %s\n\n%s", cur, v, notes(string(log), cur))
	return restart()
}

// notes is the changelog's sections above release cur's, and cur's own when
// cur is a candidate for it. A changelog without cur's section, from an older
// release or against a local build, gives none.
func notes(log, cur string) string {
	base, _, rc := strings.Cut(cur, "-")
	end := strings.Index(log, "\n## "+base+"\n") + 1
	if end == 0 {
		return ""
	}
	if rc {
		if j := strings.Index(log[end:], "\n## "); j >= 0 {
			end += j + 1
		} else {
			end = len(log)
		}
	}
	return log[strings.Index(log, "\n## ")+1 : end]
}
