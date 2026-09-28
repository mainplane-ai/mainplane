// e2e is the release gate. It installs release v from dl.mainplane.ai on real
// machines over ssh and checks each over the worker protocol, then takes them
// down to release prev and back, and past a release that does not exist. An
// rc is tagged stable only after it passes.
//
//	task e2e -- <v> <prev> <port> <os>=<ssh target>...
//
// The machines reach this one through a quick tunnel the run opens to port on
// loopback, as they reach an installed harness. Linux and macOS targets
// need passwordless sudo. The Windows target must be an elevated login, with
// the operator logged in at the console. Then each machine becomes a harness
// in one line, which updates to prev and back, and both are uninstalled: a
// run ends with every machine clean, and any harness it had is replaced and
// gone, though its config and sessions stay. Each phase is this
// program again, as a harness at one version: its exit drops every
// connection, so each worker says hello to the next.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/tunnel"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

const (
	// A phase waits for every worker to download a release and restart;
	// longer is a failure.
	phaseWait = 10 * time.Minute
	// missing is a version no release has, so an update to it fails.
	missing = "v0.0.0-e2e-missing"
)

// Scripts for each machine, by whether it is Windows.
var (
	install = map[bool]string{
		false: `curl -fsSL %[1]s%[2]s/install.sh | sudo sh -s -- '%[3]s'`,
		true:  `& ([scriptblock]::Create((irm %[1]s%[2]s/install.ps1))) '%[3]s'`,
	}
	// The key only has to be set: install checks for one, and no step calls a model.
	serverInstall = map[bool]string{
		false: `export ANTHROPIC_API_KEY=e2e-unused
curl -fsSL %[1]s%[2]s/install.sh | sh -s -- server || exit 1
/usr/local/bin/mainplane workers && echo workers-ok`,
		true: `$env:ANTHROPIC_API_KEY = 'e2e-unused'
& ([scriptblock]::Create((irm %[1]s%[2]s/install.ps1))) server
if ($LASTEXITCODE) { exit $LASTEXITCODE }
mainplane workers
if (!$LASTEXITCODE) { 'workers-ok' }`,
	}
	serverUpdate = map[bool]string{
		false: `b=/usr/local/bin/mainplane-server
sudo $b update %[1]s >/dev/null 2>&1
$b version
systemctl is-active mainplane-server 2>/dev/null || { sudo launchctl print system/ai.mainplane.server >/dev/null 2>&1 && echo active; }`,
		true: `$b = "$env:ProgramFiles\mainplane\mainplane-server.exe"
& $b update %[1]s *> $null; & $b version; if ((Get-Service mainplane-server).Status -eq 'Running') { 'active' }`,
	}
	// Each uninstall elevates itself. What is left of either is printed
	// before clean.
	uninstall = map[bool]string{
		false: `/usr/local/bin/mainplane-server uninstall >/dev/null && /usr/local/bin/mainplane uninstall >/dev/null || exit 1
ls /usr/local/bin | grep mainplane
ls /etc/systemd/system/mainplane* /Library/LaunchDaemons/ai.mainplane.* 2>/dev/null
echo clean`,
		true: `& "$env:ProgramFiles\mainplane\mainplane-server.exe" uninstall *> $null; if ($LASTEXITCODE) { exit 1 }
& "$env:ProgramFiles\mainplane\mainplane.exe" uninstall *> $null; if ($LASTEXITCODE) { exit 1 }
Get-Service mainplane* -ErrorAction SilentlyContinue | ForEach-Object Name
Get-ChildItem "$env:ProgramFiles\mainplane", "$env:LOCALAPPDATA\Programs\mainplane" -ErrorAction SilentlyContinue | ForEach-Object FullName
'clean'`,
	}
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
	exe, err := os.Executable()
	if err != nil {
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
	for _, o := range oses {
		out, err := remote(o, ssh[o], fmt.Sprintf(install[o == "windows"], release.DL, v, auth.Token(auth.Join, pointer.Encode(k), secret)))
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
	harnessAt(v, "connect")
	for _, o := range oses {
		win := o == "windows"
		out, err := remote(o, ssh[o], fmt.Sprintf(serverInstall[win], release.DL, v))
		check(o, "harness installed in one line, CLI logged in", err == nil && strings.Contains(out, "mainplane logged in to") && strings.Contains(out, "workers-ok"), last(out))
		for _, u := range []string{prev, v} {
			out, err = remote(o, ssh[o], fmt.Sprintf(serverUpdate[win], u))
			check(o, "harness updated to "+u+", running", err == nil && strings.Join(strings.Fields(out), " ") == u+" active", out)
		}
		out, err = remote(o, ssh[o], uninstall[win])
		check(o, "uninstall: no service or binary left", err == nil && strings.TrimSpace(out) == "clean", out)
	}
	if t, ok := ssh["windows"]; ok {
		out, err := remote("windows", t, fmt.Sprintf(defender, start.Format(time.RFC3339)))
		check("windows", "Defender: no detection since the run began", err == nil && strings.TrimSpace(out) == "0", out)
	}
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
// is check.
func phase(listen, url, secret, v string, oses []string, mode, text string) {
	version.V = v
	p := harness.NewPool(func(s string) bool { return s == secret })
	mux := http.NewServeMux()
	mux.Handle("/worker", p)
	mux.Handle("/id", pointer.ID(key(), func() string { return url }))
	go func() { log.Fatal(http.ListenAndServe(listen, mux)) }()
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
				check(l.OS, map[bool]string{true: "listed refused", false: "connected at " + v}[mode == "refused"], true, fmt.Sprintf("%.0fs %s %s %s", time.Since(start).Seconds(), l.Name, l.Version, l.Refused))
			}
		}
	}
	if mode == "check" {
		for _, o := range oses {
			r, ok := p.Get(got[o].Name)
			check(o, "still connected", ok, got[o].Name)
			if ok {
				checks(r, o)
			}
		}
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
	if goos != "windows" {
		out, _ := sh("stat -c %U " + file + " " + o.Full + " 2>/dev/null || stat -f %Su " + file + " " + o.Full + "; id -un")
		f := strings.Fields(out)
		check(goos, "written and spilled files owned by the operator", len(f) == 3 && f[0] == f[2] && f[1] == f[2], out)
	}
}
