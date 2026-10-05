package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
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
)

// NFSv4.0, the newest the macOS client speaks before macOS 26, so locks are
// the server's and no credential is stored. hard: a server that is away is
// waited for, never answered with an error a program takes for the file's.
// intr: a run that is stopped can still be killed while it waits, as on
// Linux. actimeo=1: a change on another worker shows within a second, as on
// Linux. resvport: a Linux export takes only a port below 1024, which the
// Linux client uses and the macOS one only when asked.
const mountOpts = "vers=4.0,hard,intr,actimeo=1,resvport"

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
	for _, p := range slices.Sorted(maps.Keys(at)) {
		if ours(p, at[p]) && !wanted[p] {
			if err := unmount(p, false); err != nil {
				have = append(have, Drive{Name: filepath.Base(p), Path: p, State: Failed, Error: "no longer this worker's, still mounted: " + err.Error()})
			}
		}
	}
	return have
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
	ctx, cancel := context.WithTimeout(context.Background(), mountWait)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "mount", "-t", "nfs", "-o", opts, nfs, p).CombinedOutput(); err != nil {
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
// from a drive server's path for that name. Anything else under /Volumes is
// the machine's.
func ours(p, src string) bool { return strings.HasSuffix(src, ":"+exportPath(filepath.Base(p))) }

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
	ctx, cancel := context.WithTimeout(context.Background(), mountWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "umount", args...).CombinedOutput()
	if _, ok := mountpoints()[p]; ok {
		return fmt.Errorf("umount: %w: %s", err, bytes.TrimSpace(out))
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
