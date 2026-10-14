//go:build !windows

package worker

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
)

// prepare puts cmd in its own process group, so killTree reaches every process
// it started. Under a root service it runs cmd as the operator: their uid, gid,
// and groups, in their home, with an environment of their own and none of
// root's. The login shell's profile adds the rest of their PATH.
func prepare(cmd *exec.Cmd, op *user.User) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = os.Environ()
	if op == nil {
		return nil
	}
	cmd.Dir = op.HomeDir
	cmd.Env = []string{"HOME=" + op.HomeDir, "USER=" + op.Username, "LOGNAME=" + op.Username, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	// A root service's children are outside the operator's login session, so
	// on macOS nothing on screen answers them: screencapture, open, and
	// osascript fail. launchctl asuser joins the session as root, chroot to /
	// drops to the operator, and env returns to their home, which chroot left.
	// All three exec, so cmd's pid is the interpreter.
	if runtime.GOOS == "darwin" {
		cmd.Args = append([]string{"launchctl", "asuser", op.Uid, "/usr/sbin/chroot", "-u", op.Uid, "-g", op.Gid, "/", "/usr/bin/env", "-C", op.HomeDir, cmd.Path}, cmd.Args[1:]...)
		cmd.Path = "/bin/launchctl"
		return nil
	}
	ids, err := op.GroupIds()
	if err != nil {
		return err
	}
	// Every id is checked: one read as zero would be root's.
	nums := make([]uint32, len(ids)+2)
	for i, id := range append([]string{op.Uid, op.Gid}, ids...) {
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return fmt.Errorf("operator %s: %w", op.Username, err)
		}
		nums[i] = uint32(n)
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: nums[0], Gid: nums[1], Groups: nums[2:]}
	return nil
}

func killTree(p *os.Process) { _ = syscall.Kill(-p.Pid, syscall.SIGKILL) }

// Work is the worker: on the mesh, with its state in the state directory
// under a service and in scratch when run by hand, and dialing the harness,
// until the service manager stops it. Then it leaves the mesh, so no
// address, route, rule or hosts block stays behind.
func Work(key string, l Local) error {
	dir := filepath.Join(l.Scratch, "mesh")
	if l.Operator != nil {
		dir = filepath.Join(stateDir, "mesh")
	}
	// A stop while the mesh comes up waits for it, so Close still runs.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	m, err := mesh.Up(dir, key, l.Secret, l.Name)
	if err != nil {
		return err
	}
	Dial(m, l)
	log.Printf("%v: leaving the mesh", <-stop)
	if l.Operator != nil {
		leave(m)
	}
	return m.Close()
}

// restart becomes the new binary in place, same pid and arguments, so a
// service and a worker started by hand both come back as the new release.
func restart(exe string) {
	log.Fatal(syscall.Exec(exe, os.Args, os.Environ()))
}
