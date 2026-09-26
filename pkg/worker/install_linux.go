package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	stateDir = "/var/lib/mainplane"
	unitPath = "/etc/systemd/system/mainplaned.service"
)

const unit = `[Unit]
Description=mainplane worker
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s worker
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`

// Install makes this machine a worker at every boot: a systemd unit running
// Bin as root. Logs are in journalctl -u mainplaned.
func Install(token string) error {
	if err := setup(token); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, fmt.Appendf(nil, unit, Bin), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "mainplaned"); err != nil {
		return err
	}
	return run("systemctl", "restart", "mainplaned")
}

// unregister stops the worker and removes its unit; none installed is fine.
// A stop that fails is an error, since the worker would run on.
func unregister() error {
	if _, err := os.Stat(unitPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := run("systemctl", "disable", "--now", "mainplaned"); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	return run("systemctl", "daemon-reload")
}
