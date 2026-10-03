package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The harness is a Windows service as LocalSystem, which needs no one logged
// in. Install needs an elevated shell. The harness stops before the binary is
// placed, because Windows cannot replace a running one.
const (
	name = "mainplane-server"
	// Dir and all under it: SYSTEM, Administrators and the operator full
	// control, nothing inherited and no one else.
	sddl = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;%s)"
	// A harness that fails starts again after 5 s, then every minute; a day
	// without failures resets the count.
	firstRestart, laterRestart, resetAfter = 5 * time.Second, time.Minute, 24 * 60 * 60
	// A stopping harness closes its listeners and returns; longer is a hang.
	stopWait = 30 * time.Second
)

// x/sys/windows does not wrap SendMessageTimeout. A broadcast waits on every
// top-level window, and one that hangs is skipped.
const (
	hwndBroadcast   = 0xffff
	wmSettingChange = 0x1a
	smtoAbortIfHung = 2
)

var (
	Dir     = filepath.Join(os.Getenv("ProgramData"), "mainplane-server")
	bin     = filepath.Join(os.Getenv("ProgramFiles"), "mainplane", "mainplane-server.exe")
	logPath = filepath.Join(Dir, "mainplane-server.log")

	sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
)

// Up runs the harness until Ctrl+C, or under the service manager until it
// says stop, with logs and panics in Dir\mainplane-server.log.
func Up(path string) error {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return Harness(ctx, path)
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
	return svc.Run(name, service{path})
}

type service struct{ path string }

// Execute runs the harness for the service manager. A harness that fails
// exits 1, which the recovery actions restart.
func (s service) Execute(_ []string, reqs <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Harness(ctx, s.path) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			log.Printf("harness: %v", err)
			cancel()
			return false, 1
		case r := <-reqs:
			//exhaustive:ignore the service accepts stop and shutdown only
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

// prepare stops a running harness and makes Dir, which only SYSTEM,
// Administrators and the operator may read: the config in it holds the
// provider keys, and ProgramData lets every user read by default. The
// operator is the user who installs, as the admin worker's is, so admin, whose
// code runs unelevated as them, reads and edits the config and keys. A
// protected DACL drops every entry an earlier install or someone else left on
// Dir, and an empty unprotected one on each child drops its own, so it
// inherits Dir's alone.
func prepare() error {
	if err := stop(); err != nil {
		return err
	}
	op, err := user.Current()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(sddl, op.Uid))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(Dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	if sd, err = windows.SecurityDescriptorFromString("D:"); err != nil {
		return err
	}
	empty, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return filepath.WalkDir(Dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil || p == Dir {
			return err
		}
		return windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, empty, nil)
	})
}

// start runs the harness at every boot as the service mainplane-server, which
// a later install reconfigures.
func start() error {
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
		BinaryPathName: syscall.EscapeArg(bin) + " up " + syscall.EscapeArg(Conf),
		DisplayName:    "Mainplane Server",
		Description:    "The Mainplane harness: sessions, workers and the API. Config in " + Conf + ", logs in " + logPath,
	}
	s, err := m.OpenService(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		s, err = m.CreateService(name, bin, c, "up", Conf)
	} else if err == nil {
		err = s.UpdateConfig(c)
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: firstRestart}, {Type: mgr.ServiceRestart, Delay: laterRestart}}, resetAfter); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return err
	}
	if err := s.Start(); err != nil {
		return err
	}
	fmt.Println("note: the harness stops while this machine sleeps; powercfg /change standby-timeout-ac 0 keeps it awake on power")
	return path(true)
}

// Uninstall stops and deletes the service, removes the worker admin and the
// binary, and takes bin's folder off PATH once nothing else is in it. Dir
// stays: it holds the sessions.
func Uninstall() error {
	if err := stop(); err != nil {
		return err
	}
	if err := uninstallAdmin(); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(name)
	if err == nil {
		err = s.Delete()
		_ = s.Close()
	}
	if err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	for _, f := range []string{bin, bin + ".old", bin + ".new"} {
		if err := remove(f); err != nil {
			return err
		}
	}
	switch err := os.Remove(filepath.Dir(bin)); {
	case err == nil || errors.Is(err, fs.ErrNotExist):
		return path(false)
	case errors.Is(err, windows.ERROR_DIR_NOT_EMPTY):
		return nil
	default:
		return err
	}
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

func restart() error {
	if err := stop(); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	return s.Start()
}

// path puts bin's folder on the machine PATH, or takes it off, and tells
// running programs, so a shell Explorer starts next sees the change. The
// value stays unexpanded, since other entries may name variables.
func path(on bool) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer func() { _ = k.Close() }()
	p, _, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}
	dir := filepath.Dir(bin)
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
