package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	stateDir  = "/Library/Application Support/mainplane"
	label     = "ai.mainplane.mainplaned"
	plistPath = "/Library/LaunchDaemons/" + label + ".plist"
	logPath   = "/var/log/mainplaned.log"
)

const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>worker</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`

// Install makes this machine a worker at every boot: a LaunchDaemon running
// Bin as root. Logs are in /var/log/mainplaned.log.
func Install(token string) error {
	if err := setup(token); err != nil {
		return err
	}
	_ = run("launchctl", "bootout", "system/"+label)
	if err := os.WriteFile(plistPath, fmt.Appendf(nil, plist, label, Bin, logPath, logPath), 0o644); err != nil {
		return err
	}
	return run("launchctl", "bootstrap", "system", plistPath)
}

// unregister stops the worker and removes its plist; none installed is fine.
func unregister() error {
	_ = run("launchctl", "bootout", "system/"+label)
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
