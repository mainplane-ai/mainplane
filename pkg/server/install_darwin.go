package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

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
<key>ProgramArguments</key><array><string>%s</string><string>up</string><string>%s</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`

// start runs the harness at every boot: a LaunchDaemon, as root. Logs are in
// /var/log/mainplane-server.log.
func start() error {
	_ = run("launchctl", "bootout", "system/"+label)
	if err := os.WriteFile(plistPath, fmt.Appendf(nil, plist, label, bin, Conf, logPath, logPath), 0o644); err != nil {
		return err
	}
	return run("launchctl", "bootstrap", "system", plistPath)
}

func restart() error { return run("launchctl", "kickstart", "-k", "system/"+label) }

// unregister stops the harness and removes its plist; none installed is fine.
// bootout fails for a daemon not loaded too, so the check is whether it is
// still loaded after: then it would run on.
func unregister() error {
	err := run("launchctl", "bootout", "system/"+label)
	if run("launchctl", "print", "system/"+label) == nil {
		return errors.Join(fmt.Errorf("%s is still loaded", label), err)
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
