//go:build !windows

package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Bin is the binary the service runs. It and the state directory are root's:
// code that runs as the operator cannot replace the service or read its token.
const Bin = "/usr/local/bin/mainplane"

// sudoers are the groups sudo lets act as root on Debian, Fedora, and macOS.
// Code runs as the operator, so an operator in one can become root, and
// install says so instead of letting the root service suggest otherwise.
var sudoers = []string{"sudo", "wheel", "admin"}

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
// who ran sudo. The binary goes to Bin, the token and the operator's name to
// the state directory, and the operator's scratch is theirs. A token an older
// install left in scratch is removed.
func setup(token string) error {
	name := os.Getenv("SUDO_USER")
	if os.Geteuid() != 0 || name == "" {
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
	if err := os.Lchown(scratch, uid, gid); err != nil {
		return err
	}
	ids, err := u.GroupIds()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if g, err := user.LookupGroupId(id); err == nil && slices.Contains(sudoers, g.Name) {
			fmt.Printf("note: %s is in group %s, so code on this worker can use sudo to act as root\n", name, g.Name)
		}
	}
	return nil
}

// Uninstall stops the worker and removes its service, Bin, which is also the
// CLI, and the state directory with the join token. Scratch stays: it is the
// operator's.
func Uninstall() error {
	if err := unregister(); err != nil {
		return err
	}
	for _, f := range []string{Bin, Bin + ".old"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(stateDir)
}

// run is one service manager command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
