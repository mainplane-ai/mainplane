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
	// needrestart (run by unattended-upgrades) restarts services after
	// library updates; a mainplaned restart kills every run and job.
	needrestartPath = "/etc/needrestart/conf.d/mainplane.conf"
	needrestartConf = "$nrconf{override_rc}{qr(^mainplaned\\.service$)} = 0;\n"
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
	if err := os.MkdirAll("/etc/needrestart/conf.d", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(needrestartPath, []byte(needrestartConf), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "mainplaned"); err != nil {
		return err
	}
	if err := run("systemctl", "restart", "mainplaned"); err != nil {
		return err
	}
	done()
	return nil
}

// unregister stops the worker and removes its unit; none installed is fine.
// A stop that fails is an error, since the worker would run on.
func unregister() error {
	if err := os.Remove(needrestartPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
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
