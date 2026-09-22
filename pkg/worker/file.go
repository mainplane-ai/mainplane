package worker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A root worker reads and writes files as the operator, so a request reaches
// no file the operator's own code could not. For each one it runs its own
// binary as the operator, `mainplane file read|write <path>`, with the file on
// stdout or stdin. A worker that is its operator does the work itself.

func (s *server) read(path string) ([]byte, error) {
	if s.Operator == nil {
		return os.ReadFile(path)
	}
	cmd, err := s.file("read", path)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fail(err, &stderr)
	}
	return b, nil
}

// create opens path for writing, making its directory. The error of a write
// the operator may not make comes from Close.
func (s *server) create(path string) (io.WriteCloser, error) {
	if s.Operator == nil {
		return createFile(path)
	}
	cmd, err := s.file("write", path)
	if err != nil {
		return nil, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	h := &helper{WriteCloser: in, cmd: cmd}
	cmd.Stderr = &h.stderr
	return h, cmd.Start()
}

func (s *server) file(op, path string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "file", op, path)
	return cmd, prepare(cmd, s.Operator)
}

type helper struct {
	io.WriteCloser
	cmd    *exec.Cmd
	stderr bytes.Buffer
}

func (h *helper) Close() error {
	_ = h.WriteCloser.Close()
	if err := h.cmd.Wait(); err != nil {
		return fail(err, &h.stderr)
	}
	return nil
}

// fail is the helper's own words when it said any: they name the file and
// the reason, as a direct read or write would.
func fail(err error, stderr *bytes.Buffer) error {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return errors.New(msg)
	}
	return err
}

// File is the helper's side, run as the operator: read copies path to stdout,
// write copies stdin to path and makes its directory.
func File(op, path string) error {
	switch op {
	case "read":
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(os.Stdout, f)
		return err
	case "write":
		f, err := createFile(path)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, os.Stdin); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
	return fmt.Errorf("file: unknown op %q", op)
}

// createFile makes path's directory and opens path for writing, 0644 as the
// worker's writes have always been.
func createFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}
