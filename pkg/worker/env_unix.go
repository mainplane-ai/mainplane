//go:build !windows

package worker

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"syscall"
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
	groups := make([]uint32, len(ids))
	for i, id := range ids {
		n, _ := strconv.ParseUint(id, 10, 32)
		groups[i] = uint32(n)
	}
	// macOS refuses more than 16 groups, and a Mac user is in about as many;
	// past 16 only its membership daemon answers, which exec cannot ask for.
	if runtime.GOOS == "darwin" {
		groups = groups[:min(len(groups), 16)]
	}
	uid, _ := strconv.ParseUint(op.Uid, 10, 32)
	gid, _ := strconv.ParseUint(op.Gid, 10, 32)
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}
	cmd.Dir = op.HomeDir
	cmd.Env = []string{"HOME=" + op.HomeDir, "USER=" + op.Username, "LOGNAME=" + op.Username, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	return nil
}

func killTree(p *os.Process) { _ = syscall.Kill(-p.Pid, syscall.SIGKILL) }
