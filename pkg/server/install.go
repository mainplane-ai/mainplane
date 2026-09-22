package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Conf is the config the installed harness runs from.
var Conf = filepath.Join(Dir, "config.json")

// Install makes this machine run the harness at every boot, from a copy of c
// at Conf. Provider values are expanded now, because a service has no user
// environment, so Dir is root's alone: the copy holds the keys. A relative
// admin moves under Dir.
func Install(c Config) error {
	for name, p := range c.Providers {
		for _, s := range []*string{&p.Key, &p.URL, &p.Region} {
			if *s != "" && os.ExpandEnv(*s) == "" {
				return fmt.Errorf("provider %s: %s is empty; sudo drops the environment, so use sudo -E or put the value in the config", name, *s)
			}
			*s = os.ExpandEnv(*s)
		}
		c.Providers[name] = p
	}
	if _, err := providers(c.Providers); err != nil {
		return err
	}
	if !filepath.IsAbs(c.Admin) {
		c.Admin = filepath.Join(Dir, c.Admin)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := prepare(); err != nil {
		return err
	}
	if err := os.WriteFile(Conf, b, 0o600); err != nil {
		return err
	}
	if err := place(); err != nil {
		return err
	}
	return start()
}

// place copies this binary to bin through a new file and a rename: a running
// binary can be replaced but not written. Installing from bin leaves it. The
// mode is set apart from the write, which the installer's umask would narrow.
func place() error {
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
	return os.Rename(bin+".new", bin)
}

// run is one service manager command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
