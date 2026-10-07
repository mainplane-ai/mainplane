// e2e is the release gate. It installs release v from dl.mainplane.ai on real
// machines over ssh and checks each over the worker protocol, then takes them
// down to release prev and back, and past a release that does not exist. An
// rc is tagged stable only after it passes.
//
//	infisical run --env=dev --path=/llm -- task e2e -- <v> <prev> <port> <os>=<ssh target>...
//
// The machines reach this one through a quick tunnel the run opens to port on
// loopback, as they reach an installed harness, and dial the harness on its
// mesh, so prev must be a release whose workers do. Linux and macOS targets
// need passwordless sudo. The Windows target must be an elevated login, with
// the operator logged in at the console. Then each machine becomes a harness
// and its worker admin in one line, a first install from no Dir, with every
// file in Dir the operator's, the harness updates to prev and back, and its
// uninstall takes both: a run ends with every machine clean, and any harness
// it had is gone, though its Dir, config and sessions, is put back. Each harness also runs the
// README quickstart with the lines its install printed, pointed at v: on its
// own machine the worker and login lines say it is already both; the next
// machine runs each twice, then sets an OpenAI key from OPENAI_API_KEY and
// starts a session on admin with mainplane new and chat. Each phase is this
// program again, as a harness at one version: its exit drops every
// connection, so each worker says hello to the next.
//
// The mesh is checked on the way: each machine's interface, hosts block and
// Tailscale after the first phase; with no links, each worker sees only the
// harness; linked, as every phase does, reach between every pair by long name,
// mainplane status, endpoints, and an ephemeral node's view in it; workers
// back within restartWait of a restart; one worker removed in the last
// phase; and nothing of the mesh left after uninstall.
//
// Drives too: in the first phase the Linux worker serves one to every
// worker (drives in the e2e func); the Linux harness gives its admin the
// sessions drive, read-only; and nothing of drives is left after uninstall.
package main

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"tailscale.com/net/tsaddr"
	"tailscale.com/tsnet"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/coordinator"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	mpmesh "github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/relay"
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/tunnel"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

const (
	// A phase waits for every worker to download a release and restart;
	// longer is a failure.
	phaseWait = 10 * time.Minute
	// missing is a version no release has, so an update to it fails.
	missing = "v0.0.0-e2e-missing"
	// A harness restarted on the same URL has its workers back in about 8s
	// (measured); a worker that takes a minute reads as a failure.
	restartWait = 30 * time.Second
	// Peers lost a removed worker within a second (measured). Its own worker
	// learns at its next poll, so the harness stays up for linger after.
	removeWait = 10 * time.Second
	linger     = 5 * time.Second
	// A first drive on a fresh server installs the NFS server and Samba
	// with apt, and a Windows client may fail one 10s round before the
	// server has its user.
	driveWait = 5 * time.Minute
	// A file made elsewhere can read as missing on Windows for 5s (the SMB
	// client's not-found cache), in a listing elsewhere for 1s (actimeo).
	driveLag = 15 * time.Second
	// A server whose NFS server just started takes no new lock for its
	// grace, 20s, so a holder can take that long to have its lock.
	lockWait = time.Minute
)

// Scripts for each machine, by whether it is Windows.
var (
	install = map[bool]string{
		false: `curl -fsSL %[1]s%[2]s/install.sh | sh -s -- '%[3]s'`,
		true:  `& ([scriptblock]::Create((irm %[1]s%[2]s/install.ps1))) '%[3]s'`,
	}
	// harnessDir is each OS's harness Dir. The harness install is a first one:
	// aside moves a Dir there to Dir.e2e, unless one from a run that stopped
	// is there, and back puts it back after the uninstall.
	harnessDir = map[string]string{"linux": "/var/lib/mainplane-server", "darwin": "/Library/Application Support/mainplane-server", "windows": `$env:ProgramData\mainplane-server`}
	aside      = map[bool]string{
		false: `d="%s"; if [ -e "$d.e2e" ]; then sudo rm -rf "$d"; elif [ -e "$d" ]; then sudo mv "$d" "$d.e2e"; fi`,
		true:  `$d = "%s"; if (Test-Path $d) { if (Test-Path "$d.e2e") { Remove-Item -Recurse -Force $d } else { Move-Item $d "$d.e2e" } }`,
	}
	back = map[bool]string{
		false: `d="%s"; sudo rm -rf "$d"; [ ! -e "$d.e2e" ] || sudo mv "$d.e2e" "$d"`,
		true:  `$d = "%s"; if (Test-Path $d) { Remove-Item -Recurse -Force $d }; if (Test-Path "$d.e2e") { Move-Item "$d.e2e" $d }`,
	}
	// No provider key: install works without one; the quickstart sets one.
	// Workers reach the harness on the mesh only: /worker, with a good key, is 404.
	// The install makes the machine the worker admin, which joins once it
	// finds the harness through the pointer. Dir and every file in it are the
	// operator's, with sessions in it: a file root wrote is one the harness
	// may not read.
	serverInstall = map[bool]string{
		false: `curl -fsSL %[1]s%[2]s/install.sh | sh -s -- server || exit 1
/usr/local/bin/mainplane workers && echo workers-ok
for i in $(seq 90); do /usr/local/bin/mainplane workers | grep -q '^admin ' && { echo admin-ok; break; }; sleep 1; done
d=/var/lib/mainplane-server; [ -d $d ] || d="/Library/Application Support/mainplane-server"
root=$(find "$d" ! -user "$(id -un)" 2>&1)
[ -z "$root" ] && cat "$d/config.json" >/dev/null && touch "$d/config.json" "$d/sessions/e2e" && rm "$d/sessions/e2e" && echo dir-ok || echo "not the operator's: $root"
l=~/.mainplane/login.json
curl -s -o /dev/null -w 'worker-%%{http_code}\n' -H "Authorization: Bearer $(sed 's/.*"key":"\([^"]*\)".*/\1/' $l)" "$(sed 's/.*"url":"\([^"]*\)".*/\1/' $l)/worker"`,
		true: `& ([scriptblock]::Create((irm %[1]s%[2]s/install.ps1))) server
if ($LASTEXITCODE) { exit $LASTEXITCODE }
mainplane workers
if (!$LASTEXITCODE) { 'workers-ok' }
for ($i = 0; $i -lt 90; $i++) { if (mainplane workers | Select-String '^admin ') { 'admin-ok'; break }; Start-Sleep 1 }
$d = "$env:ProgramData\mainplane-server"
if ((Test-Path "$d\sessions") -and ((Get-Acl $d).Access | Where-Object { $_.IdentityReference.Value -like "*\$env:USERNAME" -and $_.FileSystemRights -eq 'FullControl' -and $_.AccessControlType -eq 'Allow' })) { 'dir-ok' }
$l = Get-Content "$HOME\.mainplane\login.json" | ConvertFrom-Json
try { Invoke-WebRequest "$($l.url)/worker" -Headers @{ Authorization = "Bearer $($l.key)" } -UseBasicParsing | Out-Null; 'worker-200' } catch { "worker-$([int]$_.Exception.Response.StatusCode)" }
` + oneCLI,
	}
	serverUpdate = map[bool]string{
		false: `b=/usr/local/bin/mainplane-server
sudo $b update %[1]s >/dev/null 2>&1
$b version
systemctl is-active mainplane-server 2>/dev/null || { sudo launchctl print system/ai.mainplane.server >/dev/null 2>&1 && echo active; }`,
		true: `$b = "$env:ProgramFiles\mainplane\mainplane-server.exe"
& $b update %[1]s *> $null; & $b version; if ((Get-Service mainplane-server).Status -eq 'Running') { 'active' }`,
	}
	// The harness uninstall elevates itself and takes the worker admin with
	// it. What is left of either, the worker's state, the mesh and drives
	// (%s, from meshGone and drivesGone) is printed before clean.
	uninstall = map[bool]string{
		false: `/usr/local/bin/mainplane-server uninstall >/dev/null || exit 1
ls /usr/local/bin | grep mainplane
ls /etc/systemd/system/mainplane* /Library/LaunchDaemons/ai.mainplane.* 2>/dev/null
ls -d /var/lib/mainplane "/Library/Application Support/mainplane" /var/run/mainplaned.sock 2>/dev/null
%s
echo clean`,
		true: `& "$env:ProgramFiles\mainplane\mainplane-server.exe" uninstall *> $null; if ($LASTEXITCODE) { exit 1 }
Get-Service mainplane* -ErrorAction SilentlyContinue | ForEach-Object Name
Get-ChildItem "$env:ProgramFiles\mainplane", "$env:LOCALAPPDATA\Programs\mainplane" -ErrorAction SilentlyContinue | ForEach-Object FullName
Get-Item "$env:ProgramData\mainplane" -ErrorAction SilentlyContinue | ForEach-Object FullName
%s
'clean'`,
	}
	// meshUp prints the mesh interface and its fd7c address, only when the
	// rest is there too: on Linux the rule ahead of Tailscale's table, on
	// Windows the firewall rule that lets peers in.
	meshUp = map[string]string{
		"linux":   `ip -6 rule | grep -q 'lookup 5200' && ip -6 -o addr show dev mainplane0 scope global | awk '{print $2, $4}'`,
		"darwin":  `ifconfig | awk '/^utun/ {i = $1} /inet6 fd7c/ {print i, $2}'`,
		"windows": `if (Get-NetFirewallRule -DisplayName Mainplane -ErrorAction SilentlyContinue) { Get-NetIPAddress -InterfaceAlias Mainplane -AddressFamily IPv6 | Where-Object IPAddress -like 'fd7c*' | ForEach-Object { "Mainplane $($_.IPAddress)" } }`,
	}
	// meshGone prints what is left of the mesh's interface, address, route,
	// rule and firewall rule.
	meshGone = map[string]string{
		"linux":   `ip link show mainplane0 2>/dev/null; ip -6 rule | grep 'lookup 5200'; ip -6 route show table 5200 2>/dev/null`,
		"darwin":  `ifconfig | grep 'inet6 fd7c'; netstat -rn -f inet6 | grep '^fd7c'`,
		"windows": `Get-NetAdapter -Name Mainplane -ErrorAction SilentlyContinue | ForEach-Object Name; Get-NetRoute -AddressFamily IPv6 | Where-Object DestinationPrefix -like 'fd7c*' | ForEach-Object DestinationPrefix; Get-NetFirewallRule -DisplayName Mainplane -ErrorAction SilentlyContinue | ForEach-Object DisplayName`,
	}
	// drivesGone prints what is left of drives after uninstall: our exports
	// and nfs.conf (the smbd unit and state are under the paths uninstall
	// lists), a mount, or, from the operator's logon session, where a
	// scheduled task runs, a mapping or credential of a mesh name.
	drivesGone = map[string]string{
		"linux":  `ls /etc/exports.d/mainplane.exports /etc/nfs.conf.d/mainplane.conf 2>/dev/null; grep ' /drives/' /proc/mounts`,
		"darwin": `mount | grep fd7c`,
		"windows": `$f = "$env:ProgramData\mainplane-e2e.txt"
$a = New-ScheduledTaskAction -Execute cmd.exe -Argument "/c (net use & cmdkey /list) | findstr /i mainplane.net > $f"
Register-ScheduledTask mainplane-e2e -Action $a -Principal (New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited) -Force | Out-Null
Start-ScheduledTask mainplane-e2e
for ($i = 0; $i -lt 50 -and !((Test-Path $f) -and (Get-ScheduledTask mainplane-e2e).State -eq 'Ready'); $i++) { Start-Sleep -Milliseconds 200 }
Unregister-ScheduledTask mainplane-e2e -Confirm:$false
if (Test-Path $f) { Get-Content $f; Remove-Item $f } else { 'no probe ran in the operator session' }`,
	}
	// sessionsDrive gives the worker admin the sessions drive in the
	// harness config, which the harness reads when it changes, and prints
	// its mount options, that it lists, and that a write is refused. The
	// first time, admin installs the NFS server and Samba.
	sessionsDrive = `d=/var/lib/mainplane-server/config.json
python3 -c 'import json, sys; c = json.load(open(sys.argv[1])); c["drives"] = {"sessions": {"workers": ["admin"]}}; open(sys.argv[1], "w").write(json.dumps(c))' $d
for i in $(seq 300); do findmnt -n /drives/sessions >/dev/null && break; sleep 1; done
findmnt -n -o OPTIONS /drives/sessions | cut -d, -f1
ls /drives/sessions >/dev/null && echo listed
touch /drives/sessions/e2e 2>&1 | grep -q 'Read-only file system' && echo refused`
	hostsFile = map[bool]string{false: `cat /etc/hosts`, true: `[IO.File]::ReadAllText("$env:SystemRoot\System32\drivers\etc\hosts")`}
	tailscale = map[string]string{
		"linux":   `tailscale status >/dev/null && echo ok`,
		"darwin":  `/Applications/Tailscale.app/Contents/MacOS/Tailscale status >/dev/null && echo ok`,
		"windows": `& "$env:ProgramFiles\Tailscale\tailscale.exe" status *> $null; if (!$LASTEXITCODE) { 'ok' }`,
	}
	// Run by a worker, as its operator. A ping reply line has the peer's
	// fd7c address and a time; each command ends within seconds.
	ping      = map[string]string{"linux": "ping -6 -c 3 -W 2 %s", "darwin": "ping6 -c 3 %s", "windows": "ping -6 -n 3 -w 2000 %s"}
	status    = map[bool]string{false: "/usr/local/bin/mainplane status", true: `& "$env:ProgramFiles\mainplane\mainplane.exe" status`}
	hostsPath = map[bool]string{false: "/etc/hosts", true: `C:\Windows\System32\drivers\etc\hosts`}
	// Run by a worker on a drive, as its operator. A macOS fcntl lock over
	// NFS is a lock on the server, as is a Windows byte-range lock through
	// Samba's posix locking, so each refuses the other. qqlss is macOS's
	// struct flock: start, len, pid, type, whence. A holder makes <file>.held
	// once it has the lock and holds it 20s, past driveLag, so the other
	// worker sees that file in time to try.
	put      = map[bool]string{false: `printf %%s '%[2]s' > '%[1]s'`, true: `[IO.File]::WriteAllText('%[1]s', '%[2]s')`}
	cat      = map[bool]string{false: `cat '%s'`, true: `[IO.File]::ReadAllText('%s')`}
	exists   = map[bool]string{false: `test -e '%s' && echo yes`, true: `if (Test-Path '%s') { 'yes' }`}
	lockHold = map[string]string{
		"darwin":  `perl -MFcntl -e 'open(F, ">>", $ARGV[0]) or die $!; my $l = pack("qqlss", 0, 0, 0, F_WRLCK, 0); fcntl(F, F_SETLKW, $l) or die $!; open(M, ">", "$ARGV[0].held") or die $!; close(M); sleep 20' '%s'`,
		"windows": `$f = [IO.File]::Open('%[1]s', 'OpenOrCreate', 'ReadWrite', 'ReadWrite'); $f.Lock(0, 1); [IO.File]::WriteAllText('%[1]s.held', ''); Start-Sleep 20; $f.Close()`,
	}
	lockTry = map[string]string{
		"darwin":  `perl -MFcntl -e 'open(F, ">>", $ARGV[0]) or die $!; my $l = pack("qqlss", 0, 0, 0, F_WRLCK, 0); print fcntl(F, F_SETLK, $l) ? "locked" : $!{EAGAIN} ? "refused" : "error: $!"' '%s'`,
		"windows": `$f = [IO.File]::Open('%s', 'OpenOrCreate', 'ReadWrite', 'ReadWrite'); try { $f.Lock(0, 1); 'locked' } catch [IO.IOException] { 'refused' } finally { $f.Close() }`,
	}
	// unmounted prints what is left of a drive on a client, from the
	// operator's session.
	unmounted = map[string]string{
		"darwin":  `mount | grep mainplane.net`,
		"windows": `net use | Select-String mainplane.net; cmdkey /list | Select-String mainplane.net`,
	}
	// unserved prints what is left of serving on a server that serves nothing.
	unserved = `ls /etc/exports.d/mainplane.exports /etc/systemd/system/mainplane-smbd.service 2>/dev/null; grep ' /drives/' /proc/mounts`
	// quickstart is the README's step 3 after the login line: an OpenAI key
	// (%[1]s), a session on admin (%[2]s), and chat with no id, which follows
	// the newest session, posts the line (%[3]s) and ends with stdin. Then the
	// session once its step is done. /usr/local/bin is not on a macOS ssh PATH.
	quickstart = map[bool]string{
		false: `PATH=/usr/local/bin:$PATH
mainplane key openai '%[1]s' && echo key-ok
id=$(echo '%[2]s' | mainplane new) || exit 1
echo '%[3]s' | mainplane chat | sed -n 1p | grep -qx "$id" && echo chat-ok
for i in $(seq 180); do mainplane info $id | grep -Eq '"status": "(closed|failed)"' && break; sleep 1; done
mainplane tail $id`,
		true: `mainplane key openai '%[1]s'; if (!$LASTEXITCODE) { 'key-ok' }
$id = '%[2]s' | mainplane new
if (!$id) { exit 1 }
if (@('%[3]s' | mainplane chat)[0] -eq $id) { 'chat-ok' }
for ($i = 0; $i -lt 180 -and (mainplane info $id | Out-String) -notmatch '"status": "(closed|failed)"'; $i++) { Start-Sleep 1 }
mainplane tail $id`,
	}
	// The README's session, and a line whose answer runs a command on admin.
	session = `{"model":"openai/gpt-6.1-sol","context_limit":200000,"workers":[{"name":"admin"}],"params":{"reasoning":{"effort":"medium","summary":"auto"}}}`
	ask     = `With the run tool on worker admin, print 6*7 using its shell. Then reply with only the number.`
	// oneCLI prints every mainplane on a Windows machine: the PATH's and this
	// user's own copy. oneWin is its line when the only one is the machine's.
	oneWin = "cli C:\\Program Files\\mainplane\\mainplane.exe\n"
	oneCLI = `"cli $((@((Get-Command mainplane -All).Source) + @((Get-Item "$env:LOCALAPPDATA\Programs\mainplane\mainplane.exe" -ErrorAction SilentlyContinue).FullName) | Sort-Object -Unique) -join ',')"`
	// workerUninstall takes a worker and its CLI off a second machine.
	workerUninstall = map[bool]string{false: `/usr/local/bin/mainplane uninstall`, true: `& "$env:ProgramFiles\mainplane\mainplane.exe" uninstall`}
)

const (
	service = `$s = Get-CimInstance Win32_Service -Filter "Name='mainplaned'"
"$($s.StartName) $($s.State) $($s.StartMode)"
((Get-Acl "$env:ProgramData\mainplane").Access | ForEach-Object { $_.IdentityReference.Value }) -join ','
Stop-Process -Id $s.ProcessId -Force
$t = Get-Date
do { Start-Sleep -Milliseconds 200; $p = (Get-CimInstance Win32_Service -Filter "Name='mainplaned'").ProcessId } while (($p -eq 0 -or $p -eq $s.ProcessId) -and ((Get-Date) - $t).TotalSeconds -lt 30)
if ($p -ne 0 -and $p -ne $s.ProcessId) { 'restarted' }`
	defender = `@(Get-MpThreatDetection | Where-Object InitialDetectionTime -gt ([datetime]::Parse('%s'))).Count`
)

var failed bool

func check(who, what string, ok bool, detail string) {
	mark := "PASS"
	if !ok {
		mark, failed = "FAIL", true
	}
	fmt.Printf("%s  %-8s %-46s %s\n", mark, who, what, strings.ReplaceAll(strings.TrimSpace(detail), "\n", " | "))
}

func main() {
	switch {
	case len(os.Args) >= 8 && os.Args[1] == "phase":
		phase(os.Args[2], os.Args[3], os.Args[4], os.Args[5], strings.Split(os.Args[6], ","), os.Args[7], strings.Join(os.Args[8:], " "))
	case len(os.Args) >= 5:
		run(os.Args[1], os.Args[2], os.Args[3], os.Args[4:])
	default:
		fmt.Fprintln(os.Stderr, "usage: e2e <v> <prev> <port> <os>=<ssh target>...")
		os.Exit(2)
	}
	if failed {
		fmt.Println("FAIL")
		os.Exit(1)
	}
	fmt.Println("ALL PASS")
}

func run(v, prev, port string, targets []string) {
	ssh := map[string]string{}
	for _, t := range targets {
		o, host, _ := strings.Cut(t, "=")
		ssh[o] = host
	}
	oses := slices.Sorted(maps.Keys(ssh))
	llm := os.Getenv("OPENAI_API_KEY")
	if llm == "" {
		log.Fatal("OPENAI_API_KEY is not set: infisical run --env=dev --path=/llm -- task e2e -- ...")
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	// Each run's mesh starts empty, so workers keep their hostnames and each
	// hosts block holds this run's nodes only.
	if err := errors.Join(os.RemoveAll(filepath.Join(dir(), "nodes.json")), os.RemoveAll(filepath.Join(dir(), "mesh"))); err != nil {
		log.Fatal(err)
	}
	// the tunnel outlives every phase, so the URL on the pointer holds; its
	// cloudflared is stopped before any exit, or it outlives the run.
	ctx, cancel := context.WithCancel(context.Background())
	urls, done := make(chan string, 1), make(chan error, 1)
	go func() {
		done <- tunnel.Quick(ctx, dir(), "http://127.0.0.1:"+port, func(url string) {
			select {
			case urls <- url:
			default:
			}
		})
	}()
	stop := func() {
		cancel()
		<-done
	}
	defer stop()
	var url string
	select {
	case url = <-urls:
	case err := <-done:
		log.Fatal(err)
	}
	fmt.Printf("== tunnel %s\n", url)
	k := key()
	if err := pointer.Publish(ctx, k, url); err != nil {
		stop()
		log.Fatal(err)
	}
	secret := rand.Text()
	start := time.Now()
	hosts := map[string][]string{} // each machine's hosts lines before the mesh
	for _, o := range oses {
		out, err := remote(o, ssh[o], hostsFile[o == "windows"])
		hosts[o], _ = hostsLines(out)
		check(o, "hosts file read before install", err == nil, fmt.Sprint(err))
		out, err = remote(o, ssh[o], fmt.Sprintf(install[o == "windows"], release.DL, v, auth.Token(auth.Join, pointer.Encode(k), secret)))
		check(o, "install "+v, err == nil && strings.Contains(out, v), last(out))
	}
	harnessAt := func(ver string, mode ...string) {
		cmd := exec.Command(exe, append([]string{"phase", "127.0.0.1:" + port, url, secret, ver, strings.Join(oses, ",")}, mode...)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		// a later phase waits on the same workers, so one failed ends the run
		if cmd.Run() != nil {
			stop()
			fmt.Println("FAIL")
			os.Exit(1)
		}
	}
	harnessAt(v, "check")
	for _, o := range oses {
		joined(o, ssh[o], hosts[o], len(oses)+1)
	}
	harnessAt(v, "restart")
	if t, ok := ssh["windows"]; ok {
		out, _ := remote("windows", t, service)
		l := strings.Split(strings.TrimSpace(out), "\n")
		l = append(l, "", "", "")
		check("windows", "service: LocalSystem, running, automatic", strings.TrimSpace(l[0]) == "LocalSystem Running Auto", l[0])
		check("windows", "service: state dir SYSTEM and Administrators", strings.TrimSpace(l[1]) == `NT AUTHORITY\SYSTEM,BUILTIN\Administrators`, l[1])
		check("windows", "service: killed, restarted", strings.TrimSpace(l[2]) == "restarted", l[2])
	}
	harnessAt(prev, "connect")
	harnessAt(v, "connect")
	harnessAt(missing, "refused", "404")
	gone := oses[len(oses)-1]
	harnessAt(v, "remove", gone)
	out, err := remote(gone, ssh[gone], meshGone[gone]+"\n"+status[gone == "windows"])
	check(gone, "removed: interface, route, rule gone; status says so", err == nil && strings.HasSuffix(strings.TrimSpace(out), "removed from the mesh by its harness"), out)
	hostsCheck(gone, ssh[gone], "removed: hosts block gone, other lines kept", hosts[gone], 0)
	for i, o := range oses {
		win := o == "windows"
		out, err := remote(o, ssh[o], fmt.Sprintf(aside[win], harnessDir[o]))
		check(o, "harness: no Dir before its first install", err == nil, out)
		out, err = remote(o, ssh[o], fmt.Sprintf(serverInstall[win], release.DL, v))
		check(o, "harness installed in one line, CLI logged in", err == nil && strings.Contains(out, "mainplane "+v+" installed") && strings.Contains(out, "mp_key_") && strings.Contains(out, "workers-ok"), last(out))
		check(o, "harness: /worker is gone (404)", strings.Contains(out, "worker-404"), last(out))
		check(o, "harness: worker admin joined", strings.Contains(out, "admin-ok"), last(out))
		check(o, "harness: Dir the operator's, sessions in it", strings.Contains(out, "dir-ok"), last(out))
		quick(ssh, oses[(i+1)%len(oses)], o, v, llm, out)
		if o == "linux" {
			out, err = remote(o, ssh[o], sessionsDrive)
			check(o, "harness: sessions drive on admin, read-only", err == nil && strings.Join(strings.Fields(out), " ") == "ro listed refused", out)
		}
		// A harness before v0.5.0 finds its files elsewhere in Dir and stops,
		// so prev is checked to be placed, and v to run again.
		for _, u := range []string{prev, v} {
			out, err = remote(o, ssh[o], fmt.Sprintf(serverUpdate[win], u))
			f := strings.Fields(out)
			// prev only placed: before v0.5.0 a harness stops on this Dir, so systemctl fails the script
			check(o, "harness updated to "+u, len(f) > 0 && f[0] == u && (u == prev || err == nil && strings.Join(f, " ") == u+" active"), out)
		}
		out, err = remote(o, ssh[o], fmt.Sprintf(uninstall[win], meshGone[o]+"\n"+drivesGone[o]))
		check(o, "uninstall: no service, binary, state, mesh or drive left", err == nil && strings.TrimSpace(out) == "clean", out)
		hostsCheck(o, ssh[o], "uninstall: hosts block gone, other lines kept", hosts[o], 0)
		out, err = remote(o, ssh[o], tailscale[o])
		check(o, "tailscale status works after uninstall", err == nil && strings.TrimSpace(out) == "ok", out)
		out, err = remote(o, ssh[o], fmt.Sprintf(back[win], harnessDir[o]))
		check(o, "harness: the Dir from before the run is back", err == nil, out)
	}
	if t, ok := ssh["windows"]; ok {
		out, err := remote("windows", t, fmt.Sprintf(defender, start.Format(time.RFC3339)))
		check("windows", "Defender: no detection since the run began", err == nil && strings.TrimSpace(out) == "0", out)
	}
}

// quick runs the README quickstart against the harness just installed on o,
// with the tokens its install printed (out) in lines pointed at v. On o the
// worker and login lines find it already both. On q each runs twice, the
// second time with nothing to do, then step 3 in the login line's shell, and
// q's worker goes again.
func quick(ssh map[string]string, q, o, v, llm, out string) {
	join, key := regexp.MustCompile(`mp_join_\S+`).FindString(out), regexp.MustCompile(`mp_key_\S+`).FindString(out)
	if o == "windows" {
		check(o, "harness: one mainplane, in Program Files", strings.Contains(out, oneWin), last(out))
	}
	line := func(m, token string) (string, error) {
		return remote(m, ssh[m], fmt.Sprintf(install[m == "windows"], release.DL, v, token))
	}
	got, err := line(o, join)
	check(o, "quickstart: worker line, already worker admin", err == nil && strings.Contains(got, "this machine is already worker admin of this mainplane-server"), last(got))
	got, err = line(o, key)
	check(o, "quickstart: login line, already logged in", err == nil && strings.Contains(got, "already logged in to this mainplane-server"), last(got))
	if q == o {
		return
	}
	win := q == "windows"
	got, err = line(q, join)
	check(q, "quickstart: worker line joins "+o, err == nil, last(got))
	// the worker says it is joined once the harness has registered it
	for i := 0; i < 60 && !strings.Contains(got, "this machine"); i++ {
		time.Sleep(time.Second)
		got, _ = remote(q, ssh[q], status[win])
	}
	got, err = line(q, join)
	check(q, "quickstart: worker line again, already worker", err == nil && strings.Contains(got, "this machine is already worker "), last(got))
	got, err = line(q, key)
	check(q, "quickstart: login line logs in", err == nil && strings.Contains(got, "logged in"), last(got))
	script := fmt.Sprintf(install[win], release.DL, v, key) + "\n" + fmt.Sprintf(quickstart[win], llm, session, ask)
	if win {
		script += "\n" + oneCLI
	}
	got, err = remote(q, ssh[q], script)
	check(q, "quickstart: login line again, already logged in", strings.Contains(got, "already logged in to this mainplane-server"), fmt.Sprint(err))
	check(q, "quickstart: key, new, chat with no id follows it", strings.Contains(got, "key-ok") && strings.Contains(got, "chat-ok"), fmt.Sprint(err))
	check(q, "quickstart: the session ran 6*7 on admin, said 42", strings.Contains(got, "exit=0\n42\n") && strings.Contains(got, "  text\n42\n"), got)
	if win {
		check(q, "quickstart: one mainplane, in Program Files", strings.Contains(got, oneWin), last(got))
	}
	got, err = remote(q, ssh[q], workerUninstall[win])
	check(q, "quickstart: worker uninstalled", err == nil && strings.Contains(got, "mainplane uninstalled"), last(got))
}

// remote runs script on a machine: sh on Linux and macOS, and Windows
// PowerShell on Windows, encoded so the ssh server's cmd leaves it alone.
func remote(goos, target, script string) (string, error) {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", target, "sh -s")
	cmd.Stdin = strings.NewReader(script)
	if goos == "windows" {
		u := utf16.Encode([]rune("$ProgressPreference = 'SilentlyContinue'\n" + script))
		b := make([]byte, 2*len(u))
		for i, c := range u {
			binary.LittleEndian.PutUint16(b[2*i:], c)
		}
		cmd = exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", target, "powershell -NoProfile -EncodedCommand "+base64.StdEncoding.EncodeToString(b))
	}
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.ReplaceAll(string(out), "\r", ""), err
}

func last(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}

// joined checks machine o is on the mesh as its operator would look: the
// interface with its address, a hosts block of n nodes with every other line
// as before, and Tailscale working beside it.
func joined(o, target string, before []string, n int) {
	out, err := remote(o, target, meshUp[o])
	check(o, "mesh: interface up with an fd7c address", err == nil && strings.Contains(out, " fd7c:"), out)
	hostsCheck(o, target, "mesh: hosts block of every node, other lines kept", before, n)
	out, err = remote(o, target, tailscale[o])
	check(o, "tailscale status works beside the mesh", err == nil && strings.TrimSpace(out) == "ok", out)
}

// hostsCheck checks the hosts file on o: every line outside the block as
// before, and n lines in the block, each an fd7c address and two names.
func hostsCheck(o, target, what string, before []string, n int) {
	out, err := remote(o, target, hostsFile[o == "windows"])
	rest, block := hostsLines(out)
	ok := err == nil && slices.Equal(rest, before) && len(block) == n
	for _, l := range block {
		ok = ok && strings.HasPrefix(l, "fd7c:") && len(strings.Fields(l)) == 3
	}
	check(o, what, ok, fmt.Sprintf("block %q; %d other lines, %d before %v", block, len(rest), len(before), err))
}

// hostsLines splits a hosts file into the lines outside the mainplane block
// and those in it, blank lines and line ends aside: Tailscale on Windows
// rewrites the file with CRLF.
func hostsLines(s string) (rest, block []string) {
	in := false
	for _, l := range strings.Split(strings.TrimPrefix(s, "\ufeff"), "\n") {
		switch l = strings.TrimSpace(l); {
		case l == "# mainplane begin":
			in = true
		case l == "# mainplane end":
			in = false
		case l == "":
		case in:
			block = append(block, l)
		default:
			rest = append(rest, l)
		}
	}
	return rest, block
}

// names is each node's long name in a hosts block, by name.
func names(block []string) map[string]string {
	m := map[string]string{}
	for _, l := range block {
		if f := strings.Fields(l); len(f) == 3 {
			m[f[1]] = f[2]
		}
	}
	return m
}

// dir is this user's own, so no one else can plant the cloudflared fetch
// runs from it, or read the harness key.
func dir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(cache, "mainplane-e2e")
}

// key is the run's harness key, the same in every phase.
func key() ed25519.PrivateKey {
	k, err := pointer.Key(filepath.Join(dir(), "harness.key"))
	if err != nil {
		log.Fatal(err)
	}
	return k
}

// phase is a harness at version v, reached at url, until one worker for each
// OS connects, or is refused with text, then checks each in full when mode
// is check. As the real one, it coordinates and relays the mesh, and its own
// node there, with its keys in dir, takes the workers. Its log, the mesh's
// mostly, goes to harness.log in dir.
func phase(listen, url, secret, v string, oses []string, mode, text string) {
	version.V = v
	f, err := os.OpenFile(filepath.Join(dir(), "harness.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	log.SetOutput(f)
	die := log.New(os.Stderr, "", log.LstdFlags) // the gate sees why a phase ended
	coord, err := coordinator.New(dir(), key(), func(s, _ string) (auth.Entry, error) {
		if s != secret && s != "ephemeral-"+secret {
			return auth.Entry{}, errors.New("join secret refused")
		}
		return auth.Entry{Ephemeral: s != secret}, nil
	})
	if err != nil {
		die.Fatal(err)
	}
	coord.Relay(url)
	derp := relay.New()
	derp.SetVerifyClientFunc(coord.Known)
	mux := http.NewServeMux()
	mux.Handle("/id", pointer.ID(key(), func() string { return url }, coord.Public().String()))
	coord.Handle(mux)
	relay.Handle(mux, derp)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		die.Fatal(err)
	}
	go func() { die.Fatal(http.Serve(ln, mux)) }()
	p := harness.NewPool(coord.Desired)
	go func() {
		ls, err := coord.Listen(context.Background(), filepath.Join(dir(), "mesh"), "http://"+listen, worker.Port)
		if err == nil {
			err = p.Serve(ls[0], coord.Node)
		}
		die.Fatal(err)
	}()
	fmt.Printf("== harness %s: %s %s\n", v, mode, text)
	start := time.Now()
	got := map[string]harness.Listed{}
	for len(got) < len(oses) {
		if time.Since(start) > phaseWait {
			check(strings.Join(oses, ","), "every worker in "+phaseWait.String(), false, fmt.Sprint(p.List()))
			os.Exit(1)
		}
		time.Sleep(time.Second)
		for _, l := range p.List() {
			ok := l.Refused == ""
			if mode == "refused" {
				ok = strings.Contains(l.Refused, text)
			}
			if _, seen := got[l.OS]; ok && !seen && slices.Contains(oses, l.OS) {
				got[l.OS] = l
				what := map[string]string{"refused": "listed refused", "restart": "back within " + restartWait.String() + " at " + v}[mode]
				check(l.OS, cmp.Or(what, "connected at "+v), mode != "restart" || time.Since(start) < restartWait, fmt.Sprintf("%.0fs %s %s %s", time.Since(start).Seconds(), l.Name, l.Version, l.Refused))
			}
		}
	}
	if mode == "check" {
		unlinked(p, got, oses)
	}
	link(coord, p, got, oses)
	switch mode {
	case "check":
		for _, o := range oses {
			r, ok := p.Get(got[o].Name)
			check(o, "still connected", ok, got[o].Name)
			if ok {
				checks(r, o)
			}
		}
		mesh(p, got, oses)
		peers, err := ephemeral(listen, secret)
		check("mesh", "an ephemeral node sees only the harness", err == nil && slices.Equal(peers, []string{worker.Harness}), fmt.Sprint(peers, err))
		drives(coord, p, got, oses)
	case "remove":
		remove(coord, p, got, oses, text)
	}
	if failed {
		os.Exit(1)
	}
	os.Exit(0)
}

// checks is what a worker must do and refuse over the protocol.
func checks(r *harness.Remote, goos string) {
	ctx := context.Background()
	defer func() { _ = r.Kill(ctx, "e2e") }()
	sh := func(code string) (string, error) {
		o, err := r.Run(ctx, "e2e", "", code)
		if err == nil && o.Exit != 0 {
			err = fmt.Errorf("exit %d", o.Exit)
		}
		return strings.ReplaceAll(string(o.Body), "\r", ""), err
	}
	scratch := r.Scratch
	file := path.Join(strings.ReplaceAll(scratch, `\`, "/"), "e2e", "hello.txt")
	big := "seq 1 60000"
	if goos == "windows" {
		big = "1..60000"
		out, err := sh("whoami; $HOME; (Get-Location).Path")
		l := strings.Fields(out)
		check(goos, "run: home is cwd and scratch's parent", err == nil && len(l) == 3 && l[1] == l[2] && strings.EqualFold(l[1]+`\.mainplane`, scratch), fmt.Sprint(out, err))
		out, _ = sh("(Get-Process -Id $PID).SessionId")
		check(goos, "run: in a logon session, not session 0", strings.TrimSpace(out) != "0", out)
		out, _ = sh("whoami /groups | Select-String 'Mandatory Level' | ForEach-Object { ($_ -split '\\s{2,}')[0] }")
		check(goos, "run: unelevated", strings.Contains(out, "Medium"), out)
		out, _ = sh("$p = Start-Process charmap -PassThru; Start-Sleep 3; $p.Refresh(); $p.MainWindowHandle -ne 0; Stop-Process $p")
		check(goos, "run: a GUI window shows on the desktop", strings.TrimSpace(out) == "True", out)
		_, err = r.Read(ctx, `C:\ProgramData\mainplane\join`)
		check(goos, "read the service's join token refused", err != nil, fmt.Sprint(err))
		err = r.Write(ctx, `C:\Program Files\mainplane\mainplane.exe`, []byte("x"))
		check(goos, "write the service binary refused", err != nil, fmt.Sprint(err))
	} else {
		out, err := sh("id -un; id -u; echo $HOME; pwd")
		l := strings.Fields(out)
		check(goos, "run: not root", err == nil && len(l) == 4 && l[1] != "0", out)
		check(goos, "run: home is cwd and scratch's parent", len(l) == 4 && l[2] == l[3] && path.Dir(scratch) == l[2], out)
		_, err = r.Read(ctx, map[string]string{"linux": "/var/lib/mainplane/join", "darwin": "/Library/Application Support/mainplane/join"}[goos])
		check(goos, "read the service's join token refused", err != nil, fmt.Sprint(err))
		err = r.Write(ctx, "/usr/local/bin/mainplane-e2e", []byte("x"))
		check(goos, "write /usr/local/bin refused", err != nil, fmt.Sprint(err))
		out, _ = sh("echo x > /usr/local/bin/mainplane 2>&1; echo $?")
		check(goos, "run: replace the binary refused", strings.TrimSpace(last(out)) != "0", out)
	}
	err := r.Write(ctx, file, []byte("hi from the harness\n"))
	check(goos, "write to scratch", err == nil, fmt.Sprint(err))
	b, err := r.Read(ctx, file)
	check(goos, "read it back", err == nil && string(b) == "hi from the harness\n", fmt.Sprintf("%q %v", b, err))
	o, err := r.Run(ctx, "e2e", "", big)
	check(goos, "big output spills to a file", err == nil && o.Full != "", o.Full)
	b, err = r.Read(ctx, o.Full)
	check(goos, "spill file readable", err == nil && strings.Count(string(b), "\n") >= 60000, fmt.Sprintf("%d bytes %v", len(b), err))
	js := func(code string) (string, error) {
		o, err := r.Run(ctx, "e2e", "js", code)
		if err == nil && o.Exit != 0 {
			err = fmt.Errorf("exit %d", o.Exit)
		}
		return strings.TrimSpace(strings.ReplaceAll(string(o.Body), "\r", "")), err
	}
	check(goos, "js: listed", slices.Contains(r.Interps, "js"), strings.Join(r.Interps, " "))
	out, err := js("globalThis.n = 41; return process.versions.bun")
	check(goos, "js: runs the pinned Bun", err == nil && out == worker.BunVersion, fmt.Sprint(out, err))
	out, err = js("return n + 1")
	check(goos, "js: globalThis lasts between runs", err == nil && out == "42", fmt.Sprint(out, err))
	out, err = js(fmt.Sprintf("const cdp = await import(%q); return typeof cdp.connect", scratch+"/js/cdp.js"))
	check(goos, "js: cdp.js imports", err == nil && out == "function", fmt.Sprint(out, err))
	if goos != "windows" {
		bun := path.Join(scratch, "js", "bun")
		out, _ := sh("stat -c %U " + file + " " + o.Full + " " + bun + " 2>/dev/null || stat -f %Su " + file + " " + o.Full + " " + bun + "; id -un")
		f := strings.Fields(out)
		check(goos, "written, spilled, and js files owned by the operator", len(f) == 4 && f[0] == f[3] && f[1] == f[3] && f[2] == f[3], out)
	}
}

// mesh checks the workers on the mesh, each as its operator: it reaches every
// other worker by the long name in its hosts block, which on Windows is the
// name Tailscale's do not shadow, and mainplane status lists every node with
// its harness responsive. No node offers a Tailscale or mesh address as an endpoint,
// in the registry or as a direct path.
func mesh(p *harness.Pool, got map[string]harness.Listed, oses []string) {
	ctx := context.Background()
	var bad []string
	for _, o := range oses {
		r, ok := p.Get(got[o].Name)
		if !ok {
			continue // "still connected" failed
		}
		win := o == "windows"
		sh := func(code string) string {
			out, _ := r.Run(ctx, "e2e-mesh", "", code)
			return strings.ReplaceAll(string(out.Body), "\r", "")
		}
		b, err := r.Read(ctx, hostsPath[win])
		_, block := hostsLines(string(b))
		long := names(block)
		want := []string{worker.Harness}
		for _, q := range oses {
			if q == o {
				continue
			}
			want = append(want, got[q].Name)
			out := sh(fmt.Sprintf(ping[o], long[got[q].Name]))
			replied := slices.ContainsFunc(strings.Split(out, "\n"), func(l string) bool { return strings.Contains(l, "fd7c:") && strings.Contains(l, "time") })
			check(o, "mesh: reach "+q+" as "+long[got[q].Name], err == nil && replied, last(out))
		}
		out := sh(status[win])
		listed, paths := parseStatus(out)
		ok = strings.Contains(out, "this machine") && !strings.Contains(out, "harness unresponsive")
		for _, n := range want {
			ok = ok && listed[n]
		}
		check(o, "mesh: status lists every node, harness responsive", ok, out)
		for _, s := range paths {
			if a, err := netip.ParseAddrPort(s); err != nil || tailnet(a.Addr()) {
				bad = append(bad, o+" path "+s)
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(dir(), "nodes.json"))
	var st struct {
		Nodes []struct {
			Name      string
			Endpoints []netip.AddrPort
		}
	}
	err = errors.Join(err, json.Unmarshal(b, &st))
	n := 0
	for _, nd := range st.Nodes {
		for _, e := range nd.Endpoints {
			if n++; tailnet(e.Addr()) {
				bad = append(bad, nd.Name+" endpoint "+e.String())
			}
		}
	}
	check("mesh", "no endpoint in Tailscale's ranges or the mesh", err == nil && n > 0 && len(bad) == 0, fmt.Sprint(n, " endpoints ", bad, err))
}

// unlinked checks that each worker, with no links, has only itself and the
// harness in its hosts block, once the block is written.
func unlinked(p *harness.Pool, got map[string]harness.Listed, oses []string) {
	for _, o := range oses {
		r, ok := p.Get(got[o].Name)
		if !ok {
			continue
		}
		var block []string
		for start := time.Now(); len(block) < 2 && time.Since(start) < removeWait; time.Sleep(200 * time.Millisecond) {
			b, _ := r.Read(context.Background(), hostsPath[o == "windows"])
			_, block = hostsLines(string(b))
		}
		_, h := names(block)[worker.Harness]
		check(o, "mesh: no links, sees only the harness", len(block) == 2 && h, fmt.Sprint(block))
	}
}

// link makes every worker see every other, as links in a config do, and
// waits until each one's hosts block lists every node.
func link(coord *coordinator.Coordinator, p *harness.Pool, got map[string]harness.Listed, oses []string) {
	var pairs [][2]string
	for _, a := range oses {
		for _, b := range oses {
			if a < b {
				pairs = append(pairs, [2]string{got[a].Name, got[b].Name})
			}
		}
	}
	coord.Link(pairs)
	for _, o := range oses {
		r, ok := p.Get(got[o].Name)
		if !ok {
			continue
		}
		var block []string
		for start := time.Now(); len(block) != len(oses)+1 && time.Since(start) < removeWait; time.Sleep(200 * time.Millisecond) {
			b, _ := r.Read(context.Background(), hostsPath[o == "windows"])
			_, block = hostsLines(string(b))
		}
		check(o, "mesh: linked, sees every node", len(block) == len(oses)+1, fmt.Sprint(block))
	}
}

// drives checks a drive the Linux worker serves to every worker: mounted at
// its OS's path on each, a file written on each read on every other, a lock
// held on the macOS client refusing the Windows client's and the other way,
// then removal: the clients' mounts and mappings go, then the server's
// exports, smbd and bind mount.
func drives(coord *coordinator.Coordinator, p *harness.Pool, got map[string]harness.Listed, oses []string) {
	if !slices.Contains(oses, "linux") {
		return
	}
	d := drive{coord: coord, p: p, got: got, at: map[string]string{}, session: "e2e-drives"}
	var all []string
	for _, o := range oses {
		all = append(all, got[o].Name)
	}
	d.set(all...)
	for _, o := range oses {
		m, ok := d.wait(o, false)
		want := map[string]string{"linux": "/drives/" + driveName, "darwin": "/Volumes/" + driveName}[o]
		d.at[o] = m.Path + "/"
		if o == "windows" {
			want, d.at[o] = m.Path, m.Path
			ok = ok && len(m.Path) == 3 && strings.HasSuffix(m.Path, `:\`)
		}
		check(o, "drive: mounted at its OS's path", ok && m.Path == want, fmt.Sprint(m))
	}
	for _, o := range oses {
		_, err := d.sh(o, fmt.Sprintf(put[o == "windows"], d.at[o]+o+".txt", "from "+o))
		check(o, "drive: write a file", err == nil, fmt.Sprint(err))
	}
	for _, o := range oses {
		for _, q := range oses {
			if q != o {
				d.read(o, q)
			}
		}
	}
	if slices.Contains(oses, "darwin") && slices.Contains(oses, "windows") {
		d.lock("darwin", "windows")
		d.lock("windows", "darwin")
	}
	d.kill(oses)         // nothing of ours holds the drive open
	d.session += "-gone" // a killed session's next run begins with "environment was reset"
	d.set(got["linux"].Name)
	for _, o := range oses {
		if o != "linux" {
			_, ok := d.wait(o, true)
			out, err := d.sh(o, unmounted[o])
			check(o, "drive: removed from it, unmounted", ok && out == "", fmt.Sprint(out, err))
		}
	}
	d.set()
	_, ok := d.wait("linux", true)
	out, err := d.sh("linux", unserved)
	check("linux", "drive: serves none, no exports, smbd or mount", ok && out == "", fmt.Sprint(out, err))
	d.kill(oses)
}

// The drive the e2e func serves.
const driveName = "e2e"

type drive struct {
	coord   *coordinator.Coordinator
	p       *harness.Pool
	got     map[string]harness.Listed
	at      map[string]string // the drive's path on each OS, with its separator
	session string            // the session its shells run in
}

// set serves the drive from the Linux worker to workers, none: no drive.
func (d drive) set(workers ...string) {
	ds := map[string]coordinator.Drive{}
	if len(workers) > 0 {
		ds[driveName] = coordinator.Drive{Server: d.got["linux"].Name, Workers: workers}
	}
	for _, err := range d.coord.SetDrives(ds) {
		check("drive", "config applied", false, err.Error())
	}
	d.p.Resend()
}

func (d drive) sh(o, code string) (string, error) {
	r, ok := d.p.Get(d.got[o].Name)
	if !ok {
		return "", errors.New("not connected")
	}
	out, err := r.Run(context.Background(), d.session, "", code)
	if err == nil && out.Exit != 0 {
		err = fmt.Errorf("exit %d", out.Exit)
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out.Body), "\r", "")), err
}

func (d drive) kill(oses []string) {
	for _, o := range oses {
		if r, ok := d.p.Get(d.got[o].Name); ok {
			_ = r.Kill(context.Background(), d.session)
		}
	}
}

// wait is o's mount of the drive once it is mounted, or, when gone, once o
// has nothing of it.
func (d drive) wait(o string, gone bool) (worker.Drive, bool) {
	for start := time.Now(); time.Since(start) < driveWait; time.Sleep(time.Second) {
		r, ok := d.p.Get(d.got[o].Name)
		if !ok {
			break
		}
		ds := r.Drives()
		i := slices.IndexFunc(ds, func(m worker.Drive) bool { return m.Name == driveName && (gone || !m.Serve) })
		if gone && i < 0 {
			return worker.Drive{}, true
		}
		if !gone && i >= 0 && ds[i].State == worker.Mounted {
			return ds[i], true
		}
	}
	return worker.Drive{}, false
}

// read checks o reads the file q wrote, within driveLag.
func (d drive) read(o, q string) {
	var out string
	for start := time.Now(); out != "from "+q && time.Since(start) < driveLag; time.Sleep(time.Second) {
		out, _ = d.sh(o, fmt.Sprintf(cat[o == "windows"], d.at[o]+q+".txt"))
	}
	check(o, "drive: reads what "+q+" wrote", out == "from "+q, out)
}

// lock checks a lock h holds refuses t's, once t sees h has it, and t
// takes it once h let go.
func (d drive) lock(h, t string) {
	f := "lock-" + h
	held := make(chan error, 1)
	go func() {
		_, err := d.sh(h, fmt.Sprintf(lockHold[h], d.at[h]+f))
		held <- err
	}()
	var seen string
	for start := time.Now(); seen != "yes" && time.Since(start) < lockWait; time.Sleep(time.Second) {
		seen, _ = d.sh(t, fmt.Sprintf(exists[t == "windows"], d.at[t]+f+".held"))
	}
	busy, _ := d.sh(t, fmt.Sprintf(lockTry[t], d.at[t]+f))
	err := <-held
	free, _ := d.sh(t, fmt.Sprintf(lockTry[t], d.at[t]+f))
	check(t, "drive: lock refused while "+h+" holds one", seen == "yes" && err == nil && busy == "refused" && free == "locked", fmt.Sprint(seen, " ", busy, " then ", free, " ", err))
}

// tailnet is whether a is in Tailscale's ranges or the mesh's own: a path
// over either would ride a tunnel inside a tunnel.
func tailnet(a netip.Addr) bool { return tsaddr.IsTailscaleIP(a) || mpmesh.Prefix.Contains(a) }

// parseStatus reads mainplane status: the names on it, and each direct path.
func parseStatus(out string) (listed map[string]bool, paths []string) {
	listed = map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 {
			listed[f[0]] = true
		}
		if len(f) >= 4 && f[2] == "direct" {
			paths = append(paths, strings.TrimSuffix(f[3], ","))
		}
	}
	return listed, paths
}

// ephemeral joins a node with an ephemeral secret, in userspace in this
// process, and returns the names of the peers it sees. Persistent workers
// must not see it either, which each one's hosts block shows after the phase.
func ephemeral(listen, secret string) ([]string, error) {
	d := filepath.Join(dir(), "ephemeral")
	if err := os.RemoveAll(d); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := &tsnet.Server{Dir: d, Hostname: "e2e-ephemeral", ControlURL: "http://" + listen, AuthKey: "ephemeral-" + secret, Logf: log.Printf, UserLogf: log.Printf}
	defer func() { _ = s.Close() }()
	if _, err := s.Up(ctx); err != nil {
		return nil, err
	}
	lc, err := s.LocalClient()
	if err != nil {
		return nil, err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	var peers []string
	for _, p := range st.Peer {
		peers = append(peers, p.HostName)
	}
	return peers, nil
}

// remove takes the worker on goos off the mesh, as mainplane worker remove
// does. Every other worker's hosts block loses it within removeWait, and it
// does not come back to the pool.
func remove(coord *coordinator.Coordinator, p *harness.Pool, got map[string]harness.Listed, oses []string, goos string) {
	ctx := context.Background()
	name := got[goos].Name
	err := coord.Remove(name)
	p.Drop(name)
	check(goos, "worker remove "+name, err == nil, fmt.Sprint(err))
	start := time.Now()
	for _, o := range oses {
		r, ok := p.Get(got[o].Name)
		if o == goos || !ok {
			continue
		}
		lost := false
		for !lost && time.Since(start) < removeWait {
			b, err := r.Read(ctx, hostsPath[o == "windows"])
			_, block := hostsLines(string(b))
			if _, in := names(block)[name]; err == nil && !in {
				lost = true
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}
		check(o, "mesh: "+name+" gone from the hosts block", lost, fmt.Sprintf("%.1fs", time.Since(start).Seconds()))
	}
	time.Sleep(linger)
	_, back := p.Get(name)
	check(goos, "removed: not back in the pool", !back, name)
}
