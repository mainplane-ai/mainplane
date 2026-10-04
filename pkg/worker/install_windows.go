package worker

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// The worker is the Windows service mainplaned as LocalSystem. It runs code
// only as its operator, the user who installed it, in their logon session.
// Install needs admin. The worker stops before the binary is placed, so it
// starts again as the new one.
const (
	name = "mainplaned"
	// stateDir and all under it: SYSTEM and Administrators full control,
	// nothing inherited and no one else, since the join token is in it.
	sddl = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	// A worker that ends starts again after 2 s, as systemd's does. An update
	// ends it on purpose, so every restart waits the same.
	restartDelay = 2 * time.Second
	stopWait     = 30 * time.Second
	machineEnv   = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	// The mesh's adapter is wintun's, the WireGuard project's signed driver,
	// the release Tailscale ships too, so one driver serves both. A bump is
	// a PR that changes both lines.
	wintunZip = "https://www.wintun.net/builds/wintun-0.14.1.zip"
	wintunSum = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
	// The zip is 0.7 MB; a download that stalls fails the install instead.
	fetchWait = time.Minute
	// Uninstall asks the service to drop its drives, since only LocalSystem
	// acts in the operator's session, where they are mapped. A stop alone
	// keeps them, for an update. 128 is the first code a service may define.
	dropControl = svc.Cmd(128)
)

// x/sys/windows does not wrap SendMessageTimeout. A broadcast waits on every
// top-level window, and one that hangs is skipped.
const (
	hwndBroadcast   = 0xffff
	wmSettingChange = 0x1a
	smtoAbortIfHung = 2
)

var (
	// Bin is the binary the service runs, in the folder mainplane-server's is.
	Bin      = filepath.Join(os.Getenv("ProgramFiles"), "mainplane", "mainplane.exe")
	stateDir = filepath.Join(os.Getenv("ProgramData"), "mainplane")
	logPath  = filepath.Join(stateDir, "mainplaned.log")
	// wintun loads wintun.dll from the folder of the exe or System32 only.
	wintunDLL = filepath.Join(filepath.Dir(Bin), "wintun.dll")
	// cliDir is where install.ps1 puts the CLI when it makes no worker.
	cliDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "mainplane")

	sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
)

// Installed is what install left for the service: the join token and the
// operator, stored as their SID.
func Installed() (token string, op *user.User, err error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "join"))
	if err != nil {
		return "", nil, err
	}
	sid, err := os.ReadFile(filepath.Join(stateDir, "operator"))
	if err != nil {
		return "", nil, err
	}
	op, err = user.LookupId(string(sid))
	return string(b), op, err
}

// Work is the worker: on the mesh and dialing the harness, under the service
// manager until it says stop, with logs in logPath, else until Ctrl-C or its
// console closes. Then it leaves the mesh. Environments end with the
// service, since their stdin closes.
func Work(key string, l Local) error {
	ok, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !ok {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		m, err := mesh.Up(filepath.Join(l.Scratch, "mesh"), key, l.Secret, l.Name)
		if err != nil {
			return err
		}
		Dial(m, l)
		log.Printf("%v: leaving the mesh", <-stop)
		return m.Close()
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		return err
	}
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	return svc.Run(name, service{key, l})
}

type service struct {
	key string
	l   Local
}

// Execute is the worker on the mesh until stop, which leaves the mesh. A
// mesh that fails to come up ends the process, and the service manager
// starts it again.
func (s service) Execute(_ []string, reqs <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	m, err := mesh.Up(filepath.Join(stateDir, "mesh"), s.key, s.l.Secret, s.l.Name)
	if err != nil {
		log.Fatalf("mesh: %v", err)
	}
	ds := Dial(m, s.l)
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for r := range reqs {
		//exhaustive:ignore the service accepts stop and shutdown only, and its own drop
		switch r.Cmd {
		case svc.Interrogate:
			status <- r.CurrentStatus
		case dropControl:
			log.Print("uninstall: the drives go")
			ds.drop()
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			if err := m.Close(); err != nil {
				log.Printf("leaving the mesh: %v", err)
			}
			return false, 0
		}
	}
	return false, 0
}

// Install makes this machine a worker at every boot: the service mainplaned
// running Bin, with code run as the user who installed. Under UAC that is
// the user who clicked yes, unless they typed another account's password.
func Install(token string) error {
	op, err := user.Current()
	if err != nil {
		return err
	}
	// wintun.dll first: a failed download leaves the running worker as it was.
	dll, err := wintun()
	if err != nil {
		return err
	}
	if err := stop(); err != nil {
		return err
	}
	if err := place(Bin); err != nil {
		return err
	}
	if err := os.WriteFile(wintunDLL, dll, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(stateDir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "join"), []byte(token), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "operator"), []byte(op.Uid), 0o600); err != nil {
		return err
	}
	scratch := filepath.Join(op.HomeDir, ".mainplane")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(scratch, "join"))
	if err := register(); err != nil {
		return err
	}
	if err := path(registry.LOCAL_MACHINE, machineEnv, filepath.Dir(Bin), true); err != nil {
		return err
	}
	fmt.Print(version.Installed())
	return nil
}

// Uninstall has the service drop its drives, stops and deletes it, removes
// what a killed worker left of the mesh, Bin and wintun.dll, the state
// directory with the join token and the mesh keys, and the CLI install.ps1
// puts in cliDir, and takes each folder off PATH once nothing else is in it.
// Scratch stays: it is the operator's.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	// The service takes controls in order, so the stop waits for the drop.
	s, err := m.OpenService(name)
	if err == nil {
		_, err = s.Control(dropControl)
		_ = s.Close()
	}
	if err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	if err := stop(); err != nil {
		return err
	}
	s, err = m.OpenService(name)
	if err == nil {
		err = s.Delete()
		_ = s.Close()
	}
	if err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	// A hosts file that cannot be written must not keep the token and keys.
	cerr := mesh.Clean()
	cli := filepath.Join(cliDir, "mainplane.exe")
	for _, f := range []string{Bin, Bin + ".old", Bin + ".new", wintunDLL, cli, cli + ".old", cli + ".new"} {
		if err := remove(f); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(stateDir); err != nil {
		return err
	}
	for _, e := range []struct {
		root     registry.Key
		sub, dir string
	}{{registry.LOCAL_MACHINE, machineEnv, filepath.Dir(Bin)}, {registry.CURRENT_USER, "Environment", cliDir}} {
		err := os.Remove(e.dir)
		if errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := path(e.root, e.sub, e.dir, false); err != nil {
			return err
		}
	}
	return cerr
}

// remove deletes f. A running binary, as this one usually is, cannot be
// deleted but can be moved: it moves to the temp folder and goes at the next
// reboot.
func remove(f string) error {
	err := os.Remove(f)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	t := filepath.Join(os.TempDir(), fmt.Sprintf("%s.%d", filepath.Base(f), time.Now().UnixNano()))
	if err := os.Rename(f, t); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(t)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

// wintun is this machine's wintun.dll from wintunZip.
func wintun() ([]byte, error) {
	resp, err := (&http.Client{Timeout: fetchWait}).Get(wintunZip)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", wintunZip, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(b)) != wintunSum {
		return nil, fmt.Errorf("%s does not match its sha256", wintunZip)
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	f, err := z.Open("wintun/bin/" + runtime.GOARCH + "/wintun.dll")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// register runs Bin at every boot as the service mainplaned, which a later
// install reconfigures.
func register() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	// no ServiceStartName: LocalSystem on create, unchanged on update
	c := mgr.Config{
		ServiceType:    windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:      mgr.StartAutomatic,
		ErrorControl:   mgr.ErrorNormal,
		BinaryPathName: syscall.EscapeArg(Bin) + " worker",
		DisplayName:    "Mainplane Worker",
		Description:    "Runs a Mainplane harness's code as the operator in their session. Logs in " + logPath,
	}
	s, err := m.OpenService(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		s, err = m.CreateService(name, Bin, c, "worker")
	} else if err == nil {
		err = s.UpdateConfig(c)
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	// one action, which repeats, so the failure count never matters
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: restartDelay}}, 0); err != nil {
		return err
	}
	return s.Start()
}

// stop stops the service and waits until its process has ended, which can be
// a moment after it reports stopped; until then its log is open. None
// installed is stopped.
func stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	st, err := s.Query()
	if err != nil || st.State == svc.Stopped {
		return err
	}
	p, err := windows.OpenProcess(windows.SYNCHRONIZE, false, st.ProcessId)
	if err != nil {
		// it may have ended between the query and the open
		if st, qerr := s.Query(); qerr == nil && st.State == svc.Stopped {
			return nil
		}
		return err
	}
	defer func() { _ = windows.CloseHandle(p) }()
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	ev, err := windows.WaitForSingleObject(p, uint32(stopWait.Milliseconds()))
	if err == nil && ev != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("%s did not stop in %s", name, stopWait)
	}
	return err
}

// path puts dir on the PATH in the environment key root\sub, or takes it off,
// and tells running programs, so a shell Explorer starts next sees the
// change. The value stays unexpanded, since other entries may name variables.
func path(root registry.Key, sub, dir string, on bool) error {
	k, err := registry.OpenKey(root, sub, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }()
	p, _, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}
	es := strings.Split(p, ";")
	kept := slices.DeleteFunc(slices.Clone(es), func(e string) bool { return strings.EqualFold(e, dir) })
	switch {
	case on && len(kept) == len(es):
		p = strings.TrimSuffix(p, ";") + ";" + dir
	case !on && len(kept) < len(es):
		p = strings.Join(kept, ";")
	default:
		return nil
	}
	if err := k.SetExpandStringValue("Path", p); err != nil {
		return err
	}
	env, _ := windows.UTF16PtrFromString("Environment")
	_, _, _ = sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, 0)
	return nil
}
