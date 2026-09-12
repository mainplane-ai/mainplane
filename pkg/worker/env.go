package worker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// An output chunk is one frame; 32 KiB keeps frame count low without holding
// much of a fast producer back.
const chunk = 32 << 10

// An interpreter is a program that reads code on stdin. done is the line the
// worker appends after the code: it prints the mark and the exit code of what
// ran before, so the worker knows where the output ends. pwsh's $? is a bool;
// $LASTEXITCODE is the native exit code when one ran.
type interp struct {
	argv []string
	done string
}

var interps = map[string]interp{
	"bash": {[]string{"bash"}, `echo "%s $?"`},
	"pwsh": {[]string{"pwsh", "-NoProfile", "-NonInteractive", "-Command", "-"}, `"%s $(if($?){0}elseif($LASTEXITCODE){$LASTEXITCODE}else{1})"`},
}

// env is one running interpreter. Variables, cwd, and background jobs persist
// between runs. It lives until its process ends or the harness sends kill.
type env struct {
	it  interp
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	mu  sync.Mutex // one run at a time
}

func start(it interp) (*env, error) {
	cmd := exec.Command(it.argv[0], it.argv[1:]...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
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
	return &env{it: it, cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

func (e *env) kill() { _ = e.cmd.Process.Kill() }

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
	for {
		line, rerr := e.out.ReadBytes('\n')
		if bytes.HasPrefix(line, []byte(mark)) {
			exit, _ = strconv.Atoi(strings.TrimSpace(string(line[len(mark):])))
			return exit, true, flush()
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
