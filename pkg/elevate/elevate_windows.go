package elevate

import (
	"log"
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// tokenElevation is TOKEN_INFORMATION_CLASS TokenElevation, which the
	// syscall package does not name.
	tokenElevation = 20
	// console, then the asking process's id, lead the elevated child's args.
	// UAC starts it with an environment of its own, so args are the one way
	// to tell it.
	console = "--console-of"
	// ShellExecuteEx: keep the process handle, and return only once started.
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
	swHide                = 0
)

// x/sys/windows wraps neither ShellExecuteEx nor the console calls.
var (
	shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
	attachConsole  = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	freeConsole    = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")
)

// shellExecuteInfo is SHELLEXECUTEINFOW, whose layout Go's alignment matches.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       windows.Handle
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    windows.Handle
	idList     uintptr
	class      *uint16
	keyClass   windows.Handle
	hotKey     uint32
	icon       windows.Handle
	process    windows.Handle
}

// init makes an elevated child write to the console of the process that
// asked for it, in place of the hidden one UAC gave it. A child that cannot
// attach still does its work, only unseen.
func init() {
	if len(os.Args) < 3 || os.Args[1] != console {
		return
	}
	pid, _ := strconv.Atoi(os.Args[2])
	os.Args = append(os.Args[:1], os.Args[3:]...)
	_, _, _ = freeConsole.Call()
	_, _, _ = attachConsole.Call(uintptr(pid))
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return
	}
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(out.Fd()))
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(out.Fd()))
	os.Stdout, os.Stderr = out, out
	log.SetOutput(out)
}

// Is says whether this process runs elevated.
func Is() bool {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer func() { _ = t.Close() }()
	var e, n uint32
	return syscall.GetTokenInformation(t, tokenElevation, (*byte)(unsafe.Pointer(&e)), 4, &n) == nil && e != 0
}

// Run runs this binary with args elevated, which shows the UAC prompt, and
// returns its exit code. Its output shows in this console. A prompt the user
// declines is 1, and returns, so the caller's cleanup runs.
func Run(args ...string) int {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	line := console + " " + strconv.Itoa(os.Getpid())
	for _, a := range args {
		line += " " + syscall.EscapeArg(a)
	}
	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess | seeMaskNoAsync,
		verb:       windows.StringToUTF16Ptr("runas"),
		file:       windows.StringToUTF16Ptr(exe),
		parameters: windows.StringToUTF16Ptr(line),
		show:       swHide,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, err := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		log.Print(err)
		return 1
	}
	defer func() { _ = windows.CloseHandle(info.process) }()
	var code uint32
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		log.Print(err)
		return 1
	}
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		log.Print(err)
		return 1
	}
	return int(code)
}
