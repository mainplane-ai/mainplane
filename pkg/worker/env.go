package worker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
)

// An output chunk is one frame; 32 KiB keeps frame count low without holding
// much of a fast producer back.
const chunk = 32 << 10

// An interpreter is a program that reads code on stdin. done is the line the
// worker appends after the code: it prints the mark and the exit code of what
// ran before, so the worker knows where the output ends. pwsh's $? is a bool,
// so its exit is 0 or 1; the text of the failure is in the output. bash is a
// login shell: the worker is a service with the service manager's bare PATH,
// and the operator's profile is where their tools are. js is in js.go.
type interp struct {
	argv []string
	done string
}

var interps = map[string]interp{
	"bash": {[]string{"bash", "-l"}, `echo "%s $?"`},
	"pwsh": {[]string{"pwsh", "-NoProfile", "-NonInteractive", "-Command", "-"}, `"%s $(if($?){0}else{1})"`},
}

// env is one running interpreter. Variables, cwd, and background jobs persist
// between runs. It lives until its process ends or the harness sends kill.
type env struct {
	it   interp
	cmd  *exec.Cmd
	in   io.WriteCloser
	pipe io.Closer // our end of the output pipe
	out  *bufio.Reader
	mu   sync.Mutex // one run at a time
}

func start(it interp, op *user.User) (*env, error) {
	cmd := exec.Command(it.argv[0], it.argv[1:]...)
	if err := prepare(cmd, op); err != nil {
		return nil, err
	}
	// A drive's files are its owner's whoever wrote them, and git refuses a
	// repository another user owns; this trusts every one without touching
	// the operator's gitconfig.
	cmd.Env = append(cmd.Env, "NO_COLOR=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &env{it: it, cmd: cmd, in: in, pipe: out, out: bufio.NewReader(out)}, nil
}

// kill ends the interpreter and everything it started, then closes our end of
// the pipe so a run blocked reading returns even if some child kept the write
// end open. The tree kill is best effort; the interpreter's own death is not.
func (e *env) kill() {
	killTree(e.cmd.Process)
	_ = e.cmd.Process.Kill()
	_ = e.pipe.Close()
}

// run feeds code and the done line, emits output until the mark, and returns
// the exit code. If the process ends first, alive is false and exit is the
// process's. If ctx ends first, the process is killed and ctx's error returned.
func (e *env) run(ctx context.Context, code, mark string, emit func([]byte) error) (exit int, alive bool, err error) {
	stop := context.AfterFunc(ctx, e.kill)
	defer stop()
	if _, err := fmt.Fprintf(e.in, "%s\n%s\n", code, fmt.Sprintf(e.it.done, mark)); err != nil {
		_ = e.cmd.Wait()
		return e.cmd.ProcessState.ExitCode(), false, nil
	}
	var buf []byte
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		err := emit(buf)
		buf = nil
		return err
	}
	sentinel := []byte(mark + " ")
	for {
		line, rerr := e.out.ReadBytes('\n')
		// the sentinel may share a line with output that had no newline
		if i := bytes.Index(line, sentinel); i >= 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(string(line[i+len(sentinel):]))); err == nil {
				buf = append(buf, line[:i]...)
				return n, true, flush()
			}
		}
		buf = append(buf, line...)
		if len(buf) >= chunk || rerr != nil {
			if err := flush(); err != nil {
				return 0, true, err
			}
		}
		if rerr != nil {
			_ = e.cmd.Wait()
			if ctx.Err() != nil {
				return 0, false, ctx.Err()
			}
			return e.cmd.ProcessState.ExitCode(), false, nil
		}
	}
}
