package worker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// place copies this binary to bin, the path the service runs, through a new
// file and renames. The old one moves aside to .old first, since Windows
// renames a running binary but will not replace it, and a CLI or file helper
// may run from bin. Installing from bin itself leaves it. The mode is set
// apart from the write, which the installer's umask would narrow, so every
// user can run the CLI.
func place(bin string) error {
	exe, err := os.Executable()
	if err != nil || exe == bin {
		return err
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(bin+".new", b, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(bin+".new", 0o755); err != nil {
		return err
	}
	if err := os.Rename(bin, bin+".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(bin+".new", bin)
}
