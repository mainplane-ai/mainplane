package worker

import (
	"fmt"
	"os"
)

const unitPath = "/etc/systemd/system/mainplaned.service"

const unit = `[Unit]
Description=mainplane worker
After=network-online.target
Wants=network-online.target

[Service]
User=%s
ExecStart=%s worker
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`

// Install makes this machine a worker at every boot: a systemd unit running
// this binary as the operator. Logs are in journalctl -u mainplaned.
func Install(token string) error {
	exe, u, err := operator(token)
	if err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, fmt.Appendf(nil, unit, u.Username, exe), 0o644); err != nil {
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
