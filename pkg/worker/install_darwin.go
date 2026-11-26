package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"golang.org/x/mod/semver"
)

// launchd removes a daemon a few milliseconds after it exits, which an
// uninstall's may take a logout to a harness that is down (logoutWait, 5s) to do;
// half a minute later it is stuck.
const unloadWait = 30 * time.Second

const (
	stateDir  = "/Library/Application Support/mainplane"
	label     = "ai.mainplane.mainplaned"
	plistPath = "/Library/LaunchDaemons/" + label + ".plist"
	logPath   = "/var/log/mainplaned.log"
	// App is the bundle the service runs from, Bin a link to its binary, so
	// macOS privacy settings show the worker as Mainplane with its icon, and
	// keep a grant by its bundle id and Developer ID across releases.
	App    = "/Applications/Mainplane.app"
	appBin = App + "/Contents/MacOS/mainplane"
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
	if err := bundle(version.V); err != nil {
		log.Printf("the worker runs %s, not from %s: %v", Bin, App, err)
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
	done()
	return nil
}

// unregister stops the worker and removes its plist; none installed is fine.
func unregister() error {
	if err := bootout(); err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return errors.Join(os.RemoveAll(App), os.RemoveAll(App+".new"), os.RemoveAll(App+".old"))
}

// bundle puts release v's App in place and links Bin to its binary.
func bundle(v string) error {
	if err := release.InstallApp(App, v); err != nil {
		return err
	}
	if err := os.Remove(Bin + ".new"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(appBin, Bin+".new"); err != nil {
		return err
	}
	return os.Rename(Bin+".new", Bin)
}

// linked says Bin is a link into App.
func linked() bool {
	fi, err := os.Lstat(Bin)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// Upgrade makes exe release v's: Bin, once linked, by v's App, whether exe
// is the link or the binary it points to.
// Releases before firstApp have no App: Bin becomes their bare binary, as
// they installed it, and App goes.
func Upgrade(exe, v string) error {
	if (exe == Bin || exe == appBin) && linked() {
		if semver.Compare(v, firstApp) >= 0 {
			return bundle(v)
		}
		if err := release.Install(Bin, "mainplane", v); err != nil {
			return err
		}
		return os.RemoveAll(App)
	}
	return release.Install(exe, "mainplane", v)
}

// firstApp is the first release with an App.
const firstApp = "v0.6.1-rc.1"

// Rebundle moves a service that runs a bare Bin, as releases before App
// left it, into App, and restarts it from there. A build with no release, or
// a failed download, is logged and runs on as it is.
func Rebundle() {
	if _, err := os.Lstat(Bin); err != nil || linked() {
		return
	}
	if err := bundle(version.V); err != nil {
		log.Printf("the worker runs %s, not from %s: %v", Bin, App, err)
		return
	}
	restart(Bin)
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
