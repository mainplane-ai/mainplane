package worker

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
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

// Work is the worker: under the service manager until it says stop, with
// logs in logPath, else Dial. Environments end with the service, since their
// stdin closes.
func Work(addr string, l Local) error {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		if err != nil {
			return err
		}
		Dial(addr, l)
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
	return svc.Run(name, service{addr, l})
}

type service struct {
	addr string
	l    Local
}

func (s service) Execute(_ []string, reqs <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	go Dial(s.addr, s.l)
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for r := range reqs {
		//exhaustive:ignore the service accepts stop and shutdown only
		switch r.Cmd {
		case svc.Interrogate:
			status <- r.CurrentStatus
		case svc.Stop, svc.Shutdown:
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
	if err := stop(); err != nil {
		return err
	}
	if err := place(Bin); err != nil {
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
	fmt.Printf("code on this worker runs as %s, only while they are logged in\n", op.Username)
	return path()
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

// stop stops the service and waits until it has; none installed is stopped.
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
	st, err := s.Control(svc.Stop)
	if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return nil
	}
	for end := time.Now().Add(stopWait); err == nil && st.State != svc.Stopped; st, err = s.Query() {
		if time.Now().After(end) {
			return fmt.Errorf("%s did not stop in %s", name, stopWait)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// path puts Bin's folder on the machine PATH and tells running programs, so
// a shell Explorer starts next finds mainplane. The value stays unexpanded,
// since other entries may name variables.
func path() error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }()
	p, _, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}
	dir := filepath.Dir(Bin)
	for _, e := range strings.Split(p, ";") {
		if strings.EqualFold(e, dir) {
			return nil
		}
	}
	if err := k.SetExpandStringValue("Path", strings.TrimSuffix(p, ";")+";"+dir); err != nil {
		return err
	}
	env, _ := windows.UTF16PtrFromString("Environment")
	_, _, _ = sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, 0)
	return nil
}
