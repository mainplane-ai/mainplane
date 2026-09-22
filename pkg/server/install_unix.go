//go:build !windows

package server

import (
	"errors"
	"os"
)

const bin = "/usr/local/bin/mainplane-server"

// prepare makes Dir, which only root may read: the config in it holds the
// provider keys.
func prepare() error {
	if os.Geteuid() != 0 {
		return errors.New("install needs sudo: the harness runs as root at boot")
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(Dir, 0o700)
}
