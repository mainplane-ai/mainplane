package worker

import (
	"bytes"
	"context"
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
	"time"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
)

// A Linux client mounts each drive under mountsDir. The exports file and
// the nfs.conf drop-in are ours alone, so removing them undoes what we did.
const (
	mountsDir   = "/drives"
	exportsFile = "/etc/exports.d/mainplane.exports"
	nfsConf     = "/etc/nfs.conf.d/mainplane.conf"
)

// NFSv4 only: 4.0 for macOS, whose client before macOS 26 speaks no newer,
// 4.1 and 4.2 for Linux, whose sessions answer a retransmit once. No v3: its
// locks live in side daemons. A dead client's delegations go after 20s, not 90s: an agent
// between tool calls does not notice 20s. After a start, new locks wait out the grace
// in which clients reclaim theirs, which one lease is enough for: 20s, not 90s.
const nfsConfText = `# mainplane drives, written by mainplaned
[nfsd]
vers3=n
vers4=y
vers4.0=y
vers4.1=y
vers4.2=y
lease-time=20
grace-time=20
`

// Windows workers mount over SMB from an smbd of our own, with its config,
// state, passwords and unit apart from a Samba the machine may have, so
// removing them undoes it. Its users are marked by smbComment.
const (
	smbDir     = stateDir + "/smb"
	smbConf    = smbDir + "/smb.conf"
	smbService = "mainplane-smbd"
	smbUnit    = "/etc/systemd/system/" + smbService + ".service"
	smbComment = "mainplane drive client"
)

// Samba listens on no point-to-point interface, and the mesh's TUN is one,
// so smbd listens on every address and the kernel lets in only the mesh's
// /48. A reload reaches every smbd of ours: a HUP reaches only the first,
// and a client's own smbd would not see a new share. No [Install]:
// mainplaned starts it.
const smbUnitText = `[Unit]
Description=mainplane drives for Windows workers, started by mainplaned

[Service]
ExecStart=/usr/sbin/smbd --foreground --no-process-group --configfile=` + smbConf + `
ExecReload=/usr/bin/smbcontrol --configfile=` + smbConf + ` all reload-config
Restart=always
RestartSec=2
IPAddressDeny=any
IPAddressAllow=%s
`

// SMB3 only, with no NetBIOS, printers or guests. posix locking makes a
// Windows byte-range lock an fcntl lock, which NFS clients and local
// programs meet; kernel oplocks makes an oplock a kernel lease, which an NFS
// or local open breaks. Every file is the operator's, whoever wrote it, as
// over NFS.
const smbConfText = `# mainplane drives, written by mainplaned
[global]
	server role = standalone server
	smb ports = 445
	disable netbios = yes
	server min protocol = SMB3
	map to guest = never
	restrict anonymous = 2
	load printers = no
	printcap name = /dev/null
	disable spoolss = yes
	passdb backend = tdbsam:%[1]s/passdb.tdb
	private dir = %[1]s/private
	lock directory = %[1]s/lock
	state directory = %[1]s/state
	cache directory = %[1]s/cache
	pid directory = %[1]s
	ncalrpc dir = %[1]s/ncalrpc
	log file = %[1]s/log
	max log size = 1000
	posix locking = yes
	kernel oplocks = yes
	force user = %[2]s
`

// smbSet is the password each of our SMB users was given since this
// process started, so a password is set once a run, not every round.
var smbSet = map[string]string{}

// A server on the mesh, relayed too, answers a connect well within
// probeWait. A mount that takes longer than mountWait is waited for again
// next round, so one dead server does not hold up the other drives.
const (
	probeWait = 5 * time.Second
	mountWait = time.Minute
	nfsPort   = "2049"
)

// reconcile serves and mounts what want says, unmounts what it no longer
// says, and returns each drive as it is now.
func reconcile(want Desired, op *user.User) []Drive {
	have := serve(want.Serve, op)
	at := mountpoints()
	wanted := map[string]bool{}
	for _, m := range want.Mount {
		d := Drive{Name: m.Name, State: Failed, Error: "not a drive name"}
		if ValidDrive(m.Name) {
			p := filepath.Join(mountsDir, m.Name)
			src, ok := at[p]
			wanted[p] = true
			own := slices.IndexFunc(have, func(s Drive) bool { return s.Name == m.Name })
			d = mount(m, p, own >= 0, own >= 0 && have[own].State == Serving, src, ok)
		}
		have = append(have, d)
	}
	for _, p := range slices.Sorted(maps.Keys(at)) {
		if !wanted[p] {
			if err := unmount(p, false); err != nil {
				have = append(have, Drive{Name: filepath.Base(p), Path: p, State: Failed, Error: "no longer this worker's, still mounted: " + err.Error()})
			}
		}
	}
	return have
}

// serve makes this machine export es, each to its clients' mesh addresses
// only, every client squashed to op, so every file on a drive is op's
// whoever wrote it, and share it over SMB to its Windows clients. Serving
// nothing removes our exports and our smbd.
func serve(es []Export, op *user.User) []Drive {
	if len(es) == 0 {
		if err := errors.Join(unexport(), unsmb()); err != nil {
			log.Printf("drives: %v", err)
		}
		return nil
	}
	err := nfsServer()
	if err != nil { // a server that cannot be set up serves no one, not the clients of an older list
		err = errors.Join(err, unexport(), unsmb())
	}
	have, lines := []Drive{}, []string{"# mainplane drives, written by mainplaned"}
	shares, users := "", map[string]string{}
	for _, e := range es {
		d := Drive{Name: e.Name, Serve: true, State: Serving}
		if ValidDrive(e.Name) {
			d.Path = exportPath(e.Name)
		}
		switch {
		case !ValidDrive(e.Name):
			d.State, d.Error = Failed, "not a drive name"
		case err != nil:
			d.State, d.Error = Failed, err.Error()
		default:
			if derr := makeDir(d.Path, e.Name, op); derr != nil {
				d.State, d.Error = Failed, derr.Error()
			} else if len(e.Clients) > 0 { // a line with no clients would export to everyone
				lines = append(lines, exportLine(d.Path, e, op))
				shares += share(d.Path, e, users)
			}
		}
		have = append(have, d)
	}
	if err == nil {
		err := write(exportsFile, strings.Join(lines, "\n")+"\n", "exportfs", "-ra")
		if err == nil {
			err = smbServer(shares, users, op)
		}
		if err != nil {
			for i := range have {
				have[i].State, have[i].Error = Failed, err.Error()
			}
		}
	}
	return have
}

// share is e's section of our smb.conf, for its Windows clients, whose
// users and passwords it adds to users. None: no section, since one with no
// valid users would take any.
func share(path string, e Export, users map[string]string) string {
	var valid []string
	for _, c := range e.Clients {
		if c.SMB != nil {
			valid = append(valid, c.SMB.User)
			users[c.SMB.User] = c.SMB.Password
		}
	}
	if len(valid) == 0 {
		return ""
	}
	ro := "no"
	if e.Name == Sessions {
		ro = "yes"
	}
	return fmt.Sprintf("[%s]\n\tpath = %s\n\tread only = %s\n\tvalid users = %s\n", e.Name, path, ro, strings.Join(valid, " "))
}

// smbServer serves shares from our smbd, with a nologin user for each
// Windows client. The samba package, installed here, would start the
// distro's smbd on every address, and it was off (preflight): it stays off.
func smbServer(shares string, users map[string]string, op *user.User) error {
	if _, err := exec.LookPath("smbd"); err != nil {
		if err := apt("samba"); err != nil {
			return err
		}
		if err := run("systemctl", "disable", "--now", "smbd", "nmbd"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(smbDir+"/private", 0o700); err != nil {
		return err
	}
	p, err := mesh.Network()
	if err != nil {
		return err
	}
	if err := write(smbUnit, fmt.Sprintf(smbUnitText, p), "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := write(smbConf, fmt.Sprintf(smbConfText, smbDir, op.Username)+shares, "systemctl", "reload-or-restart", smbService); err != nil {
		return err
	}
	if err := smbUsers(users); err != nil {
		return err
	}
	// Started at the first round after a boot, or after a stop it did not
	// come back from.
	out, err := exec.Command("ss", "-Hltn", "sport", "=", ":445").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ss: %w: %s", err, out)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return run("systemctl", "start", smbService)
	}
	return nil
}

// smbUsers makes users, by name, our smbd's, with their passwords, each a
// system user that cannot log in, and removes ours no longer in users.
func smbUsers(users map[string]string) error {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return err
	}
	var errs []error
	for line := range strings.Lines(string(b)) {
		if f := strings.Split(line, ":"); len(f) > 4 && f[4] == smbComment && users[f[0]] == "" {
			errs = append(errs, run("smbpasswd", "-c", smbConf, "-x", f[0]), run("userdel", f[0]))
			delete(smbSet, f[0])
		}
	}
	for u, pw := range users {
		if smbSet[u] == pw {
			continue
		}
		if _, err := user.Lookup(u); err != nil {
			if err := run("useradd", "--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", smbComment, u); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		cmd := exec.Command("smbpasswd", "-c", smbConf, "-s", "-a", u)
		cmd.Stdin = strings.NewReader(pw + "\n" + pw + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("smbpasswd %s: %w: %s", u, err, out))
			continue
		}
		smbSet[u] = pw
	}
	return errors.Join(errs...)
}

// unsmb stops our smbd and removes its users, state and unit; the package
// stays. Each step waits for the one before, and the unit goes last, so a
// step that fails is tried again next round.
func unsmb() error {
	if _, err := os.Stat(smbUnit); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := run("systemctl", "stop", smbService); err != nil {
		return err
	}
	if err := smbUsers(nil); err != nil {
		return err
	}
	if err := os.RemoveAll(smbDir); err != nil {
		return err
	}
	if err := os.Remove(smbUnit); err != nil {
		return err
	}
	return run("systemctl", "daemon-reload")
}

func exportLine(path string, e Export, op *user.User) string {
	mode := "rw"
	if e.Name == Sessions {
		mode = "ro"
	}
	line := path
	for _, c := range e.Clients {
		line += fmt.Sprintf(" %s(%s,sync,all_squash,anonuid=%s,anongid=%s,no_subtree_check)", c.Addr, mode, op.Uid, op.Gid)
	}
	return line
}

// makeDir makes a drive's directory, op's. Sessions is the harness's own,
// so a machine without it is not the harness's.
func makeDir(path, name string, op *user.User) error {
	if name == Sessions {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("sessions is served by the harness's own machine, from %s: %w", path, err)
		}
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	uid, _ := strconv.Atoi(op.Uid)
	gid, _ := strconv.Atoi(op.Gid)
	return os.Lchown(path, uid, gid)
}

// nfsServer makes this machine an NFS server the first time a drive names it:
// the preflight, the package, our nfs.conf. Our exports file marks a machine
// that passed.
func nfsServer() error {
	if _, err := os.Stat(exportsFile); err != nil {
		if err := preflight(); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath("exportfs"); err != nil {
		if err := apt("nfs-kernel-server"); err != nil {
			return err
		}
	}
	return write(nfsConf, nfsConfText, "systemctl", "restart", "nfs-server")
}

// preflight refuses a machine that already serves files to others: knfsd is
// one per machine, so our nfs.conf would change their exports too, and SMB
// for Windows workers needs port 445 to itself.
func preflight() error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return errors.New("refused: a drive server is Debian or Ubuntu, and this machine has no apt")
	}
	files, _ := filepath.Glob("/etc/exports.d/*.exports")
	for _, f := range append([]string{"/etc/exports"}, files...) {
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for line := range strings.Lines(string(b)) {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				return fmt.Errorf("refused: %s exports %q, and drives would set this machine's NFS server to v4 only with a 20s lease; serve drives from another Linux worker, or remove that export", f, line)
			}
		}
	}
	out, err := exec.Command("ss", "-Hltnp", "sport", "=", ":445").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ss: %w: %s", err, out)
	}
	if out = bytes.TrimSpace(out); len(out) > 0 {
		return fmt.Errorf("refused: another program listens on port 445, which drives need for Windows workers: %s", out)
	}
	return nil
}

// mount puts drive m at p: from this machine's own disk when it serves m,
// since knfsd and its own client on one machine can deadlock, else over NFS
// from m's server. src is the NFS source of what is mounted at p now, empty
// for a bind mount, and ok whether anything is. A server that does not
// answer is waited for, never worked around.
func mount(m Mount, p string, own, served bool, src string, ok bool) Drive {
	d := Drive{Name: m.Name, Path: p}
	nfs := ""
	if !own {
		nfs = fmt.Sprintf("[%s]:%s", m.Addr, exportPath(m.Name))
	}
	if ok && src == nfs {
		d.State = Mounted
		if !own && !reach(m.Addr) {
			d.State, d.Error = Waiting, "server unreachable, the mount waits for it"
		}
		return d
	}
	if ok { // the drive moved to another server
		if err := unmount(p, false); err != nil {
			d.State, d.Error = Failed, err.Error()
			return d
		}
	}
	switch {
	case own && !served:
		d.State, d.Error = Failed, "this machine serves it and refused, see its serve entry"
		return d
	case !own && !reach(m.Addr):
		d.State, d.Error = Waiting, "server unreachable"
		return d
	}
	// Root's and read-only while nothing is mounted on it, so a write meant
	// for the drive fails instead of landing on local disk as a second copy.
	if err := os.MkdirAll(mountsDir, 0o755); err != nil {
		d.State, d.Error = Failed, err.Error()
		return d
	}
	if err := os.Mkdir(p, 0o555); err != nil && !errors.Is(err, fs.ErrExist) {
		d.State, d.Error = Failed, err.Error()
		return d
	}
	opts, src := "hard,actimeo=1", nfs
	if own {
		opts, src = "bind", exportPath(m.Name)
	}
	if m.Name == Sessions {
		opts += ",ro"
	}
	args := []string{"-o", opts, src, p}
	if !own {
		args = append([]string{"-t", "nfs4"}, args...)
		if _, err := exec.LookPath("mount.nfs4"); err != nil {
			if err := apt("nfs-common"); err != nil {
				d.State, d.Error = Failed, err.Error()
				return d
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), mountWait)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "mount", args...).CombinedOutput(); err != nil {
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

// mountpoints is every mount under mountsDir and its NFS source, empty for
// any other, from the kernel's table: a stat of a mount whose server is
// gone would hang.
func mountpoints() map[string]string {
	at := map[string]string{}
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return at
	}
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		i := slices.Index(f, "-")
		if len(f) < 5 || i < 0 || i+2 >= len(f) || filepath.Dir(f[4]) != mountsDir {
			continue
		}
		at[f[4]] = ""
		if strings.HasPrefix(f[i+1], "nfs") {
			at[f[4]] = f[i+2]
		}
	}
	return at
}

// unmount takes the drive at p away and its mount point with it. One that
// is in use stays and says so, unless force, for a worker that is leaving:
// then it is detached at once and goes when the last user lets go. Neither
// the NFS helper nor a canonical path is asked for: both touch the mount,
// which hangs while its server is away. The mount table says whether it
// went: an NFS mount leaves it at once, then its end tells the server and
// waits, past mountWait, for one no longer reachable, as when this worker
// left the drive and so the server's sight. Cut short, the server forgets
// this client after one lease.
func unmount(p string, force bool) error {
	args := []string{"-i", "-c", p}
	if force {
		args = []string{"-i", "-c", "-f", "-l", p}
	}
	ctx, cancel := context.WithTimeout(context.Background(), mountWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "umount", args...).CombinedOutput()
	if _, ok := mountpoints()[p]; ok {
		return fmt.Errorf("umount: %w: %s", err, bytes.TrimSpace(out))
	}
	return os.Remove(p)
}

// unexport stops serving every drive. The packages stay: they may serve
// something else by then. The file is emptied first and removed only once
// exportfs took that, so a failed exportfs is tried again next round.
func unexport() error {
	if _, err := os.Stat(exportsFile); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.WriteFile(exportsFile, nil, 0o644); err != nil {
		return err
	}
	if err := run("exportfs", "-ra"); err != nil {
		return err
	}
	return os.Remove(exportsFile)
}

// dropDrives unmounts every drive and stops serving, for a worker that left
// the mesh or is uninstalled.
func dropDrives(*user.User) error {
	var errs []error
	for _, p := range slices.Sorted(maps.Keys(mountpoints())) {
		errs = append(errs, unmount(p, true))
	}
	if err := os.Remove(nfsConf); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(append(errs, unexport(), unsmb())...)
}

// write puts text in path when it differs, then runs then. A failed then
// takes the file away again, so the next round writes and runs it again.
func write(path, text string, then ...string) error {
	if b, err := os.ReadFile(path); err == nil && string(b) == text {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	if err := run(then[0], then[1:]...); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

// apt installs pkg, for a drive that needs it.
func apt(pkg string) error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return fmt.Errorf("this machine has no apt to install %s, which drives need", pkg)
	}
	for _, args := range [][]string{{"update"}, {"install", "-y", "--no-install-recommends", pkg}} {
		cmd := exec.Command("apt-get", args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("apt-get %s: %w: %s", strings.Join(args, " "), err, out[max(0, len(out)-500):])
		}
	}
	return nil
}
