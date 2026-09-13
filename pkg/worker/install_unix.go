//go:build !windows

package worker

import (
	"errors"
	"os"
	"os/user"
)

// operator is the user who ran install under sudo: the service is root's to
// write, the worker runs as them.
func operator() (exe string, u *user.User, err error) {
	exe, err = os.Executable()
	if err != nil {
		return "", nil, err
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return "", nil, errors.New("install needs sudo: the service is root's, the worker runs as you")
	}
	u, err = user.Lookup(name)
	return exe, u, err
}
