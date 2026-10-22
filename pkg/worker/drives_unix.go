//go:build !windows

package worker

import (
	"path/filepath"

	"github.com/mainplane-ai/mainplane/pkg/pointer"
)

// A Linux server serves each drive from its own directory under exportsDir,
// and Linux and macOS clients mount it from there over NFS.
const exportsDir = "/srv/mainplane"

// exportPath is where a Linux server keeps the drive name.
func exportPath(name string) string {
	if name == Sessions {
		return filepath.Join(pointer.LinuxDir, Sessions)
	}
	return filepath.Join(exportsDir, name)
}
