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
	groups := nums[2:]
	// macOS refuses more than 16 groups, and a Mac user is in about as many;
	// past 16 only its membership daemon answers, which exec cannot ask for.
	if runtime.GOOS == "darwin" {
		groups = groups[:min(len(groups), 16)]
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: nums[0], Gid: nums[1], Groups: groups}
	cmd.Dir = op.HomeDir
	cmd.Env = []string{"HOME=" + op.HomeDir, "USER=" + op.Username, "LOGNAME=" + op.Username, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	return nil
}

func killTree(p *os.Process) { _ = syscall.Kill(-p.Pid, syscall.SIGKILL) }

// Work is the worker: on the mesh, with its state in the state directory
// under a service and in scratch when run by hand, and dialing the harness,
// until the service manager stops it. Then it leaves the mesh, so no
// address, route or rule stays behind.
func Work(key string, l Local) error {
	dir := filepath.Join(l.Scratch, "mesh")
	if l.Operator != nil {
		dir = filepath.Join(stateDir, "mesh")
	}
	m, err := mesh.Up(dir, key, l.Secret, l.Name)
	if err != nil {
		return err
	}
	go Dial(key, l)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	log.Printf("%v: leaving the mesh", <-stop)
	m.Close()
	return nil
}

// restart becomes the new binary in place, same pid and arguments, so a
// service and a worker started by hand both come back as the new release.
func restart(exe string) {
	log.Fatal(syscall.Exec(exe, os.Args, os.Environ()))
}
