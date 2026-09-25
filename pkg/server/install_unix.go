//go:build !windows

package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
)

const bin = "/usr/local/bin/mainplane-server"

// Up runs the harness until SIGINT or SIGTERM, which systemd and launchd
// send to stop it.
func Up(c Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Harness(ctx, c)
}

// Uninstall stops the harness and removes its service and binary. Dir stays:
// it holds the sessions.
func Uninstall() error {
	if err := unregister(); err != nil {
		return err
	}
	for _, f := range []string{bin, bin + ".old"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// prepare makes Dir, which only root may read: the config in it holds the
// provider keys.
func prepare() error {
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(Dir, 0o700)
}
