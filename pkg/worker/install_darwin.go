package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// launchd removes a daemon a few milliseconds after bootout returns; seconds
// later it is stuck.
const unloadWait = 5 * time.Second

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
	if err := bootout(); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, fmt.Appendf(nil, plist, label, Bin, logPath, logPath), 0o644); err != nil {
		return err
	}
	if err := run("launchctl", "bootstrap", "system", plistPath); err != nil {
		return err
	}
	return done()
}

// unregister stops the worker and removes its plist; none installed is fine.
func unregister() error {
	if err := bootout(); err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// bootout stops the worker and unloads it; none loaded is fine. bootout
// fails for a daemon not loaded too, and returns before launchd removes one
// that was, so the check is whether it is still loaded within unloadWait:
// then it would run on.
func bootout() error {
	err := run("launchctl", "bootout", "system/"+label)
	for end := time.Now().Add(unloadWait); run("launchctl", "print", "system/"+label) == nil; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			return errors.Join(fmt.Errorf("%s is still loaded", label), err)
		}
	}
	return nil
}
