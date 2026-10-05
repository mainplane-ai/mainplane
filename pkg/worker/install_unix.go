//go:build !windows

package worker

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// Bin is the binary the service runs. It and the state directory are root's:
// code that runs as the operator cannot replace the service or read its token.
const Bin = "/usr/local/bin/mainplane"

// Installed is what install left for the service: the join token and the
// operator, the user code runs as.
func Installed() (token string, op *user.User, err error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "join"))
	if err != nil {
		return "", nil, err
	}
	name, err := os.ReadFile(filepath.Join(stateDir, "operator"))
	if err != nil {
		return "", nil, err
	}
	op, err = user.Lookup(string(name))
	return string(b), op, err
}

// setup is the install both service managers share. The operator is the user
// who ran sudo, or root in a root shell with no sudo. The binary goes to Bin,
// the token and the operator's name to the state directory, and the
// operator's scratch is theirs. A token an older install left in scratch is
// removed.
func setup(token string) error {
	name := cmp.Or(os.Getenv("SUDO_USER"), "root")
	if os.Geteuid() != 0 {
		return errors.New("install needs sudo: the service is root's, code runs as you")
	}
	u, err := user.Lookup(name)
	if err != nil {
		return err
	}
	if err := place(Bin); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "join"), []byte(token), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "operator"), []byte(name), 0o644); err != nil {
		return err
	}
	scratch := filepath.Join(u.HomeDir, ".mainplane")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	_ = os.Remove(filepath.Join(scratch, "join"))
	return os.Lchown(scratch, uid, gid)
}

// done says the install is done.
func done() { fmt.Print(version.Installed()) }

// Uninstall stops the worker and removes its service, its drives, what a
// killed worker left of the mesh, Bin, which is also the CLI, and the state
// directory with the join token and the mesh keys. Scratch stays, it is the
// operator's, and so do the files of the drives this machine served.
func Uninstall() error {
	// The drives go while the worker's mesh still reaches their servers: an
	// NFS mount's end tells its server, and waits for one it cannot reach.
	// Once more after the stop, for one a last reconcile made.
	derr := dropDrives(nil)
	// so the worker logs out as it stops: see leave
	if err := os.Remove(filepath.Join(stateDir, "join")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := unregister(); err != nil {
		return err
	}
	// A hosts file that cannot be written must not keep the token and keys.
	cerr := errors.Join(derr, dropDrives(nil), mesh.Clean())
	for _, f := range []string{Bin, Bin + ".old", Bin + ".new"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return errors.Join(os.RemoveAll(stateDir), cerr)
}

// run is one service manager command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
