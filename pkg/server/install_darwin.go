package server

import (
	"fmt"
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
