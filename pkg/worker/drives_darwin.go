package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
)

// A macOS worker mounts each drive over NFS at /Volumes/<name>, where Finder
// lists it, beside the machine's own volumes. A server on the mesh, relayed
// too, answers a connect well within probeWait. A mount that takes longer
// than mountWait is waited for again next round.
const (
	mountsDir = "/Volumes"
	probeWait = 5 * time.Second
	mountWait = time.Minute
	nfsPort   = "2049"
	// osascript with the bridge starts in well under a second.
	favWait = 30 * time.Second
)

// NFSv4.0, the newest the macOS client speaks before macOS 26, so locks are
// the server's and no credential is stored. hard: a server that is away is
// waited for, never answered with an error a program takes for the file's.
// intr: a run that is stopped can still be killed while it waits, as on
// Linux. actimeo=1: a change on another worker shows within a second, as on
// Linux. resvport: a Linux export takes only a port below 1024, which the
// Linux client uses and the macOS one only when asked. inet6: only the
// mesh address, never another the server's name resolves to, such as its
// Tailscale IPv4 address. An address the server does not export to makes
// the macOS 26 kernel panic when the client then moves on to the next one.
const mountOpts = "vers=4.0,hard,intr,actimeo=1,resvport,inet6"

// The macOS client marks a mount not responding after this many seconds
// without a reply, and macOS then offers Disconnect All, which drops unsaved
// writes. The default, 5 s, is shorter than the 5-9 s the mesh takes to come
// back after a wake. A mount takes the value when it is made.
const downDelay = "vfs.generic.nfs.client.initialdowndelay=30"

// reconcile mounts what want says, unmounts what it no longer says, and
// returns each drive as it is now.
func reconcile(want Desired, _ *user.User) []Drive {
	var have []Drive
	for _, e := range want.Serve {
		have = append(have, Drive{Name: e.Name, Serve: true, State: Failed, Error: "only a Linux worker serves drives"})
	}
	at := mountpoints()
	wanted := map[string]bool{}
	for _, m := range want.Mount {
		d := Drive{Name: m.Name, State: Failed, Error: "not a drive name"}
		if ValidDrive(m.Name) {
			p := filepath.Join(mountsDir, m.Name)
			wanted[p] = true
			d = mount(m, p, at[p])
		}
		have = append(have, d)
	}
	// A drive no longer this worker's is often one whose server no longer
	// serves it, so it is forced off when it does not go, before anything
	// asks it for a file: everything that does hangs.
	stuck := false
	for _, p := range slices.Sorted(maps.Keys(at)) {
		if ours(p, at[p]) && !wanted[p] {
			err := unmount(p, false)
			if err != nil {
				if ferr := unmount(p, true); ferr != nil {
					err = fmt.Errorf("%w; forced: %w", err, ferr)
				} else {
					log.Printf("drives: %v; forced it off", err)
					err = nil
				}
			}
			if err != nil {
				have = append(have, Drive{Name: filepath.Base(p), Path: p, State: Failed, Error: "no longer this worker's, still mounted: " + err.Error()})
				stuck = true
			}
		}
	}
	if !stuck { // a Favorite's lookup asks every mount, a stuck one too
		favorites(want, have)
	}
	return have
}

// bounded runs a command for at most wait. One stuck in the kernel on a mount
// whose server is gone outlives its kill; it is left behind rather than
// waited for, so the next round still comes.
func bounded(wait time.Duration, name string, args ...string) ([]byte, error) {
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(wait + probeWait):
		return nil, fmt.Errorf("%s: still running %v after it was killed", name, probeWait)
	}
}

// favsFile holds, for each user by uid, each drive whose Finder sidebar
// Favorite the worker saw to, and whether the worker added it, so one the
// user removed stays removed and one the user made stays theirs.
var favsFile = filepath.Join(stateDir, "favorites.json")

// favJS adds the Favorite for path, unless one is there, and prints added,
// or removes it. An item whose volume is not mounted resolves to nothing and
// is known by its name. LSSharedFileList is deprecated but the only API for
// the list; it needs no privacy grant, and Finder is not asked, so no prompt
// shows.
const favJS = `ObjC.import('CoreServices')
function run([verb, path]) {
  const list = $.LSSharedFileListCreate($(), $.kLSSharedFileListFavoriteItems, $())
  const items = ObjC.castRefToObject($.LSSharedFileListCopySnapshot(list, Ref()))
  for (let i = 0; i < items.count; i++) {
    const it = items.objectAtIndex(i)
    const u = ObjC.castRefToObject($.LSSharedFileListItemCopyResolvedURL(it, $.kLSSharedFileListNoUserInteraction | $.kLSSharedFileListDoNotMountVolumes, $()))
    const name = ObjC.unwrap(ObjC.castRefToObject($.LSSharedFileListItemCopyDisplayName(it)))
    if (u.isNil() ? name != path.split('/').pop() : ObjC.unwrap(u.path) != path) continue
    if (verb == 'add') return
    if ($.LSSharedFileListItemRemove(list, it)) throw new Error('could not remove ' + name)
    return
  }
  if (verb != 'add') return
  if (!$.LSSharedFileListInsertItemURL(list, $.kLSSharedFileListItemLast, $(), $(), $.NSURL.fileURLWithPath(path), $(), $())) throw new Error('could not add ' + path)
  return 'added'
}`

// favErr is the last Favorite failure logged, so one that repeats each
// round is logged once.
var favErr string

// favorites gives the user at the screen a Finder sidebar Favorite for each
// drive once, when it is first mounted, and takes the ones it added away
// when the drive is no longer this worker's. A Favorite is only a shortcut:
// a failure is logged and tried again next round, never the drive's error.
// With no one logged in at the screen it waits.
func favorites(want Desired, have []Drive) {
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat("/dev/console")
	if err != nil {
		favLog(err)
		return
	}
	uid := strconv.FormatUint(uint64(fi.Sys().(*syscall.Stat_t).Uid), 10)
	if uid == "0" { // the login window
		return
	}
	all := map[string]map[string]bool{}
	b, err := os.ReadFile(favsFile)
	if err == nil {
		err = json.Unmarshal(b, &all)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		favLog(err)
		return
	}
	seen, changed := all[uid], false
	if seen == nil {
		seen = map[string]bool{}
	}
	for n, ours := range seen {
		if slices.ContainsFunc(want.Mount, func(m Mount) bool { return m.Name == n }) {
			continue
		}
		if ours {
			if _, err := favorite(uid, "remove", filepath.Join(mountsDir, n)); err != nil {
				favLog(err)
				continue
			}
		}
		delete(seen, n)
		changed = true
	}
	for _, d := range have {
		if _, ok := seen[d.Name]; ok || d.State != Mounted {
			continue
		}
		out, err := favorite(uid, "add", d.Path)
		if err != nil {
			favLog(err)
			continue
		}
		seen[d.Name], changed = out == "added", true
	}
	if !changed {
		return
	}
	all[uid] = seen
	if b, err = json.Marshal(all); err == nil {
		err = os.WriteFile(favsFile, b, 0o644)
	}
	if err != nil {
		favLog(err)
	}
}

// favorite runs favJS as user uid, in their login session, where their
// sidebar's list is, and returns what it printed.
func favorite(uid, verb, path string) (string, error) {
	out, err := bounded(favWait, "launchctl", "asuser", uid, "/usr/bin/sudo", "-n", "-H", "-u", "#"+uid, "--",
		"/usr/bin/osascript", "-l", "JavaScript", "-e", favJS, verb, path)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", verb, path, err, bytes.TrimSpace(out))
	}
	return string(bytes.TrimSpace(out)), nil
}

func favLog(err error) {
	if s := err.Error(); s != favErr {
		favErr = s
		log.Printf("drives: Finder Favorite: %v", err)
	}
}

// mount puts drive m at p over NFS from its server. src is what is mounted
// at p now, empty for nothing. A server that does not answer is waited for,
// never worked around. By the server's name, not its address: Finder names
// the server by what comes before the first colon.
func mount(m Mount, p, src string) Drive {
	d := Drive{Name: m.Name, Path: p}
	nfs := m.Host + ":" + exportPath(m.Name)
	switch {
	case src == nfs && !reach(m.Addr):
		d.State, d.Error = Waiting, "server unreachable, the mount waits for it"
		return d
	case src == nfs:
		d.State = Mounted
		return d
	case ours(p, src): // the drive moved to another server
		if err := unmount(p, false); err != nil {
			d.State, d.Error = Failed, err.Error()
			return d
		}
	case src != "":
		d.State, d.Error = Failed, fmt.Sprintf("%s is taken by %s", p, src)
		return d
	}
	if !reach(m.Addr) {
		d.State, d.Error = Waiting, "server unreachable"
		return d
	}
	// Root's and read-only while nothing is mounted on it, so a write meant
	// for the drive fails instead of landing on local disk as a second copy.
	if err := os.Mkdir(p, 0o555); err != nil && !errors.Is(err, fs.ErrExist) {
		d.State, d.Error = Failed, err.Error()
		return d
	}
	opts := mountOpts
	if m.Name == Sessions {
		opts += ",ro"
	}
	if out, err := exec.Command("sysctl", "-w", downDelay).CombinedOutput(); err != nil {
		d.State, d.Error = Failed, fmt.Sprintf("sysctl: %v: %s", err, bytes.TrimSpace(out))
		return d
	}
	if out, err := bounded(mountWait, "mount", "-t", "nfs", "-o", opts, nfs, p); err != nil {
		d.State, d.Error = Failed, fmt.Sprintf("mount: %v: %s", err, bytes.TrimSpace(out))
		return d
	}
	d.State = Mounted
	return d
}

// reach is whether a's NFS server takes a connection.
func reach(a netip.Addr) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(a.String(), nfsPort), probeWait)
	if err == nil {
		_ = c.Close()
	}
	return err == nil
}

// ours is whether src, mounted at p, is a drive this worker mounted: NFS
// from a mesh long name and a drive server's path for that name. Anything
// else under /Volumes is the machine's.
func ours(p, src string) bool {
	return strings.HasSuffix(src, "."+mesh.Domain+":"+exportPath(filepath.Base(p)))
}

// mountpoints is every mount directly under mountsDir and its source, from
// the kernel's table without asking any file system: a stat of a mount
// whose server is gone would hang.
func mountpoints() map[string]string {
	at := map[string]string{}
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return at
	}
	st := make([]unix.Statfs_t, n)
	if n, err = unix.Getfsstat(st, unix.MNT_NOWAIT); err != nil {
		return at
	}
	for _, f := range st[:n] {
		if p := unix.ByteSliceToString(f.Mntonname[:]); filepath.Dir(p) == mountsDir {
			at[p] = unix.ByteSliceToString(f.Mntfromname[:])
		}
	}
	return at
}

// unmount takes the drive at p away and its mount point with it. One that
// is in use stays and says so, unless force, for a worker that is leaving.
// The mount table says whether it went. macOS may remove the emptied mount
// point under /Volumes itself, before we do.
func unmount(p string, force bool) error {
	args := []string{p}
	if force {
		args = []string{"-f", p}
	}
	out, err := bounded(mountWait, "umount", args...)
	if _, ok := mountpoints()[p]; ok {
		var hint string
		if bytes.Contains(out, []byte("Operation not permitted")) {
			hint = " (macOS will not unmount some drives, such as one whose server is not responding; restarting the Mac clears it)"
		}
		return fmt.Errorf("umount: %w: %s%s", err, bytes.TrimSpace(out), hint)
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// dropDrives unmounts every drive, for a worker that left the mesh or is
// uninstalled.
func dropDrives(*user.User) error {
	var errs []error
	for p, src := range mountpoints() {
		if ours(p, src) {
			errs = append(errs, unmount(p, true))
		}
	}
	return errors.Join(errs...)
}

// canServe is false: only a Linux worker serves drives.
func canServe() bool { return false }
