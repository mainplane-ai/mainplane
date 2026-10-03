package server

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
	Dir       = "/Library/Application Support/mainplane-server"
	label     = "ai.mainplane.server"
	plistPath = "/Library/LaunchDaemons/" + label + ".plist"
	logPath   = "/var/log/mainplane-server.log"
)

const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>UserName</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>up</string><string>%s</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`

// start runs the harness at every boot: a LaunchDaemon, as the operator. Logs
// are in /var/log/mainplane-server.log.
func start() error {
	u, err := operator()
	if err != nil {
		return err
	}
	if err := bootout(); err != nil {
		return err
	}
	if err := own(u, Dir); err != nil {
		return err
	}
	// launchd opens the log as the operator, and stops a harness whose log
	// root made before.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := own(u, logPath); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, fmt.Appendf(nil, plist, label, u.Username, bin, Conf, logPath, logPath), 0o644); err != nil {
		return err
	}
	return run("launchctl", "bootstrap", "system", plistPath)
}

func restart() error { return run("launchctl", "kickstart", "-k", "system/"+label) }

// unregister stops the harness and removes its plist; none installed is fine.
func unregister() error {
	if err := bootout(); err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// bootout stops the harness and unloads it; none loaded is fine. bootout
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
