package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	Dir      = "/var/lib/mainplane-server"
	unitPath = "/etc/systemd/system/mainplane-server.service"
)

const unit = `[Unit]
Description=mainplane harness
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s up %s
User=%s
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`

// start runs the harness at every boot: a systemd unit, as the operator. Logs
// are in journalctl -u mainplane-server.
func start() error {
	u, err := operator()
	if err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, fmt.Appendf(nil, unit, bin, Conf, u.Username), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "mainplane-server"); err != nil {
		return err
	}
	if err := run("systemctl", "stop", "mainplane-server"); err != nil {
		return err
	}
	if err := own(u, Dir); err != nil {
		return err
	}
	return restart()
}

func restart() error { return run("systemctl", "restart", "mainplane-server") }

// unregister stops the harness and removes its unit; none installed is fine.
// A stop that fails is an error, since the harness would run on.
func unregister() error {
	if _, err := os.Stat(unitPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := run("systemctl", "disable", "--now", "mainplane-server"); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	return run("systemctl", "daemon-reload")
}
