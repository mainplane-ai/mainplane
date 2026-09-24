//go:build !windows

package server

import (
	"context"
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

// prepare makes Dir, which only root may read: the config in it holds the
// provider keys.
func prepare() error {
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(Dir, 0o700)
}
