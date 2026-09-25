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
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`

// start runs the harness at every boot: a systemd unit, as root. Logs are in
// journalctl -u mainplane-server.
func start() error {
	if err := os.WriteFile(unitPath, fmt.Appendf(nil, unit, bin, Conf), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "mainplane-server"); err != nil {
		return err
	}
	return restart()
}

func restart() error { return run("systemctl", "restart", "mainplane-server") }

// unregister stops the harness and removes its unit; none installed is fine.
func unregister() error {
	_ = run("systemctl", "disable", "--now", "mainplane-server")
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return run("systemctl", "daemon-reload")
}
