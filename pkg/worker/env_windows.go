package worker

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// prepare gives cmd the worker's environment. Under the service it runs cmd
// as the operator, with the token of their logon session: windows it opens
// show on their desktop, and it has their environment, PATH, and home. The
// token is their unelevated one, as in a shell they open. cmd itself gets no
// console window. The token closes when cmd is collected.
func prepare(cmd *exec.Cmd, op *user.User) error {
	cmd.Env = os.Environ()
	if op == nil {
		return nil
	}
	t, err := token(op)
	if err != nil {
		return err
	}
	runtime.AddCleanup(cmd, func(t windows.Token) { _ = t.Close() }, t)
	if cmd.Env, err = t.Environ(false); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(t), CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Dir = op.HomeDir
	if !filepath.IsAbs(cmd.Args[0]) {
		cmd.Path, cmd.Err = look(cmd.Args[0], cmd.Env)
	}
	return nil
}

// token is the operator's from a session they are logged in to, the active
// one first, where their windows show; else a disconnected one. Only
// LocalSystem may ask for one.
func token(op *user.User) (windows.Token, error) {
	var ss *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &ss, &n); err != nil {
		return 0, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(ss)))
	for _, active := range []bool{true, false} {
		for _, s := range unsafe.Slice(ss, n) {
			var t windows.Token
			if (s.State == windows.WTSActive) != active || windows.WTSQueryUserToken(s.SessionID, &t) != nil {
				continue
			}
			if u, err := t.GetTokenUser(); err == nil && u.User.Sid.String() == op.Uid {
				return t, nil
			}
			_ = t.Close()
		}
	}
	host, _ := os.Hostname()
	return 0, fmt.Errorf("operator %s is not logged in on %s", op.Username, host)
}

// look finds name on the PATH in env, the operator's, since exec looked on
// the service's.
func look(name string, env []string) (string, error) {
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); strings.EqualFold(k, "Path") {
			for _, d := range filepath.SplitList(v) {
				p := filepath.Join(d, name+".exe")
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("%s is not on the operator's PATH", name)
}

// Windows has no process groups to signal; taskkill walks the tree.
func killTree(p *os.Process) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
}

// restart makes the new binary run: Windows cannot replace a running process.
// The service exits, and the service manager starts it again from exe. A
// worker started by hand starts exe with the same arguments on its log, then
// exits.
func restart(exe string) {
	if ok, _ := svc.IsWindowsService(); ok {
		log.Printf("restarting as %s", exe)
		os.Exit(1)
	}
	c := exec.Command(exe, os.Args[1:]...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		log.Fatalf("start %s: %v", exe, err)
	}
	os.Exit(0)
}
