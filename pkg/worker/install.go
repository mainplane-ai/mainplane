package worker

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
)

// leave logs the service's node out of its harness when uninstall took the
// join token, which it does before it stops the service. The next install of
// this machine has new keys, and with the old node gone it joins under its
// own name, not <name>-2. A stop with the token, as for an update, keeps the
// node.
func leave(m *mesh.Mesh) {
	if _, err := os.Stat(filepath.Join(stateDir, "join")); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	log.Print("uninstall: logging out of the harness")
	if err := m.Logout(); err != nil {
		log.Printf("logout: %v", err)
	}
}

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
	if err := os.Rename(bin+".new", bin); err != nil {
		return errors.Join(err, os.Rename(bin+".old", bin))
	}
	return nil
}
