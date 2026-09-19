//go:build !windows

package worker

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// operator is the user who ran install under sudo: the service is root's to
// write, the worker runs as them, and the token goes in their scratch, theirs
// to read and no one else's.
func operator(token string) (exe string, u *user.User, err error) {
	exe, err = os.Executable()
	if err != nil {
		return "", nil, err
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return "", nil, errors.New("install needs sudo: the service is root's, the worker runs as you")
	}
	if u, err = user.Lookup(name); err != nil {
		return "", nil, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	path := JoinPath(u.HomeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", nil, err
	}
	if err := os.Lchown(filepath.Dir(path), uid, gid); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", nil, err
	}
	return exe, u, os.Lchown(path, uid, gid)
}
