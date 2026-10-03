//go:build !windows

package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

const bin = "/usr/local/bin/mainplane-server"

// Up runs the harness until SIGINT or SIGTERM, which systemd and launchd
// send to stop it.
func Up(path string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Harness(ctx, path)
}

// Uninstall stops the harness and removes its service and binary, and the
// worker admin. Dir stays: it holds the sessions.
func Uninstall() error {
	if err := unregister(); err != nil {
		return err
	}
	if err := uninstallAdmin(); err != nil {
		return err
	}
	for _, f := range []string{bin, bin + ".old", bin + ".new"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// prepare makes Dir, which only root and the operator may read: the config
// in it holds the provider keys.
func prepare() error {
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(Dir, 0o700)
}

// operator is the user who ran sudo. The harness runs as them, since it needs
// no root, and Dir is theirs: so every file the harness makes is theirs too,
// and admin, whose code runs as them, reads and edits the config and keys
// without sudo.
func operator() (*user.User, error) {
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return nil, errors.New("install needs sudo: the harness and its admin worker run as you")
	}
	return user.Lookup(name)
}

// own gives path and all in it to u. The harness is stopped, so it writes
// nothing as root behind.
func own(u *user.User, path string) error {
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}
