package worker

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	label     = "ai.mainplane.mainplaned"
	plistPath = "/Library/LaunchDaemons/" + label + ".plist"
)

// launchd gives a daemon no HOME, and the worker's scratch is under it. It
// opens the log paths as the daemon's user, so they must be the operator's.
const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>worker</string></array>
<key>UserName</key><string>%s</string>
<key>EnvironmentVariables</key><dict><key>HOME</key><string>%s</string></dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`

// Install makes this machine a worker at every boot: a LaunchDaemon running
// this binary as the operator. Logs are in ~/.mainplane/mainplaned.log.
func Install(token string) error {
	exe, u, err := operator(token)
	if err != nil {
		return err
	}
	log := filepath.Join(filepath.Dir(JoinPath(u.HomeDir)), "mainplaned.log")
	_ = run("launchctl", "bootout", "system/"+label)
	if err := os.WriteFile(plistPath, fmt.Appendf(nil, plist, label, exe, u.Username, u.HomeDir, log, log), 0o644); err != nil {
		return err
	}
	return run("launchctl", "bootstrap", "system", plistPath)
}
