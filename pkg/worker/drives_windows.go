package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
)

// A Windows worker maps each drive over SMB to a letter of its own, from
// firstLetter down: Windows gives disks the first free letter from C: up, and
// users map their own from Z: down. A drive keeps its letter in lettersFile
// across restarts. A server on the mesh answers a connect within probeWait;
// net and cmdkey are given mapWait.
const (
	firstLetter = 'W'
	lastLetter  = 'D'
	smbPort     = "445"
	probeWait   = 5 * time.Second
	mapWait     = time.Minute
)

var lettersFile = filepath.Join(stateDir, "drives.json")

// netUseLine is a mapping in net use's list: its letter and remote path. The
// status before them is in the OS's language.
var netUseLine = regexp.MustCompile(`(?m)\s([A-Z]:)\s+(\\\\\S+)`)

// mapped is a drive this worker mapped: its letter and its server's long name.
type mapped struct {
	Letter string `json:"letter"`
	Host   string `json:"host"`
}

// reconcile maps what want says in the operator's logon session, where
// Explorer shows it and code runs, unmaps what it no longer says, and
// returns each drive as it is now. With no operator logged in there is no
// session to map in, and the drives wait for one.
func reconcile(want Desired, op *user.User) []Drive {
	var have []Drive
	for _, e := range want.Serve {
		have = append(have, Drive{Name: e.Name, Serve: true, State: Failed, Error: "only a Linux worker serves drives"})
	}
	ours := load()
	if len(want.Mount) == 0 && len(ours) == 0 { // most workers: nothing to look at every round
		return have
	}
	at, err := mappings(op)
	if err != nil {
		for _, m := range want.Mount {
			d := Drive{Name: m.Name, State: Waiting, Error: err.Error()}
			if o, ok := ours[m.Name]; ok {
				d.Path = o.Letter + `\`
			}
			have = append(have, d)
		}
		return have
	}
	wanted := map[string]bool{}
	for _, m := range want.Mount {
		wanted[m.Name] = true
		have = append(have, mount(m, ours, at, op))
	}
	for name, o := range ours {
		if !wanted[name] {
			if err := unmap(name, o, ours, at, op, false); err != nil {
				have = append(have, Drive{Name: name, Path: o.Letter + `\`, State: Failed, Error: "no longer this worker's, still mapped: " + err.Error()})
			}
		}
	}
	if err := save(ours); err != nil {
		log.Printf("drives: %v", err)
	}
	return have
}

// mount maps drive m to its letter, given one the first time, signed in
// with the credential cmdkey keeps for its server. at is what is mapped now.
// A server that does not answer is waited for, never worked around.
func mount(m Mount, ours map[string]mapped, at map[string]string, op *user.User) Drive {
	d := Drive{Name: m.Name, State: Failed}
	if !ValidDrive(m.Name) || m.SMB == nil {
		d.Error = "not a drive name, or no SMB user from the harness"
		return d
	}
	remote := `\\` + m.Host + `\` + m.Name
	o, ok := ours[m.Name]
	if ok && o.Host != m.Host { // the drive moved to another server
		if err := unmap(m.Name, o, ours, at, op, false); err != nil {
			d.Error = err.Error()
			return d
		}
		ok = false
	}
	if !ok {
		if o.Letter = free(ours, at); o.Letter == "" {
			d.Error = fmt.Sprintf("no free drive letter from %c: to %c:", firstLetter, lastLetter)
			return d
		}
		o.Host = m.Host
		ours[m.Name] = o
	}
	d.Path = o.Letter + `\`
	if err := label(op, m.Host, m.Name, m.Name); err != nil {
		d.Error = err.Error()
		return d
	}
	switch r := at[o.Letter]; {
	case strings.EqualFold(r, remote) && !reach(m.Addr):
		d.State, d.Error = Waiting, "server unreachable, the mapping waits for it"
	case strings.EqualFold(r, remote):
		d.State = Mounted
	case r != "":
		d.Error = fmt.Sprintf("%s is taken by %s", o.Letter, r)
	case !reach(m.Addr):
		d.State, d.Error = Waiting, "server unreachable"
	default:
		if _, err := as(op, "cmdkey", "/add:"+m.Host, "/user:"+m.SMB.User, "/pass:"+m.SMB.Password); err != nil {
			d.Error = err.Error()
			return d
		}
		// Not persistent: Windows would map it at logon, before the mesh is
		// up; this worker maps it once the operator is logged in.
		_, err := as(op, "net", "use", o.Letter, remote, "/persistent:no")
		now, lerr := mappings(op)
		switch {
		case lerr != nil:
			d.Error = lerr.Error()
		case strings.EqualFold(now[o.Letter], remote):
			d.State = Mounted
		case err != nil && !ok: // the harness tells the server and this worker at once
			d.State, d.Error = Waiting, "its server may not share it with this worker yet: "+err.Error()
		case err != nil:
			d.Error = err.Error()
		default:
			d.Error = fmt.Sprintf("%s is not mapped to %s", o.Letter, remote)
		}
	}
	return d
}

// unmap takes drive name, mapped as o, away, and the credential of its
// server once no drive of ours is on it. One with open files stays and says
// so, unless force, for a worker that is leaving: then they are closed. A
// letter that holds another mapping is not ours to take.
func unmap(name string, o mapped, ours map[string]mapped, at map[string]string, op *user.User, force bool) error {
	if strings.EqualFold(at[o.Letter], `\\`+o.Host+`\`+name) {
		args := []string{"use", o.Letter, "/delete"}
		if force {
			args = append(args, "/y")
		}
		_, err := as(op, "net", args...)
		now, lerr := mappings(op)
		switch {
		case lerr != nil:
			return lerr
		case now[o.Letter] != "" && err != nil:
			return err
		case now[o.Letter] != "":
			return fmt.Errorf("%s is still mapped", o.Letter)
		}
	}
	if err := label(op, o.Host, name, ""); err != nil {
		return err
	}
	for n, p := range ours {
		if n != name && p.Host == o.Host {
			delete(ours, name)
			return nil
		}
	}
	// cmdkey fails to delete a credential that is gone already, so it looks
	// first, in the full list: one host's list names it even when it has none.
	out, err := as(op, "cmdkey", "/list")
	if err != nil {
		return err
	}
	if strings.Contains(out, "target="+o.Host) {
		if _, err := as(op, "cmdkey", "/delete:"+o.Host); err != nil {
			return err
		}
	}
	delete(ours, name)
	return nil
}

// label sets the name Explorer shows for a mapping of \\host\share, in op's
// own registry, to text alone, not "share (\\host)"; empty text takes it away.
func label(op *user.User, host, share, text string) error {
	path := op.Uid + `\Software\Microsoft\Windows\CurrentVersion\Explorer\MountPoints2\##` + host + `#` + share
	if text == "" {
		k, err := registry.OpenKey(registry.USERS, path, registry.SET_VALUE)
		if err == nil {
			err = errors.Join(k.DeleteValue("_LabelFromReg"), k.Close())
		}
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	k, _, err := registry.CreateKey(registry.USERS, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	return errors.Join(k.SetStringValue("_LabelFromReg", text), k.Close())
}

// free is the first letter from firstLetter down that no drive of ours has,
// nothing in the operator's session maps, and no disk has.
func free(ours map[string]mapped, at map[string]string) string {
	disks, _ := windows.GetLogicalDrives()
	taken := map[string]bool{}
	for _, o := range ours {
		taken[o.Letter] = true
	}
	for l := firstLetter; l >= lastLetter; l-- {
		s := string(rune(l)) + ":"
		if !taken[s] && at[s] == "" && disks&(1<<(l-'A')) == 0 {
			return s
		}
	}
	return ""
}

// mappings is each letter mapped in op's logon session and its remote path,
// from the mapping table: a look at the drive would wait on a server that
// is gone.
func mappings(op *user.User) (map[string]string, error) {
	out, err := as(op, "net", "use")
	if err != nil {
		return nil, err
	}
	at := map[string]string{}
	for _, m := range netUseLine.FindAllStringSubmatch(out, -1) {
		at[m[1]] = m[2]
	}
	return at, nil
}

// as runs a program of System32 as op, in their logon session, where a
// mapping and a credential are theirs; a worker run by hand is op already,
// and only the service may take op's token. Its error names the program and
// its first argument only, since cmdkey's carry the password, and has its
// output on one line, for status.
func as(op *user.User, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mapWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", name+".exe"), args...)
	if ok, _ := svc.IsWindowsService(); !ok {
		op = nil
	}
	if err := prepare(cmd, op); err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, args[0], err, strings.Join(strings.Fields(string(out)), " "))
	}
	return string(out), nil
}

// reach is whether a's SMB server takes a connection.
func reach(a netip.Addr) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(a.String(), smbPort), probeWait)
	if err == nil {
		_ = c.Close()
	}
	return err == nil
}

func load() map[string]mapped {
	ours := map[string]mapped{}
	b, err := os.ReadFile(lettersFile)
	if err == nil {
		err = json.Unmarshal(b, &ours)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("drives: %v", err)
	}
	return ours
}

func save(ours map[string]mapped) error {
	b, err := json.Marshal(ours)
	if err != nil {
		return err
	}
	return os.WriteFile(lettersFile, b, 0o600)
}

// dropDrives unmaps every drive this worker mapped and deletes the
// credentials, for a worker that left the mesh or is uninstalled. With the
// operator logged out the mappings are gone already and the credentials
// stay, since only their session reaches them.
func dropDrives(op *user.User) error {
	ours := load()
	at, err := mappings(op)
	if err != nil {
		return err
	}
	var errs []error
	for name, o := range ours {
		errs = append(errs, unmap(name, o, ours, at, op, true))
	}
	return errors.Join(append(errs, save(ours))...)
}
