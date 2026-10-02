package worker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/version"
)

// Local is what this machine offers: its name, the join secret that lets it
// join the mesh, where spilled output lands, and which interpreters it has.
// The first interpreter is the default. Under a root service, Operator is the
// user code runs and files are touched as; nil is the worker's own user.
type Local struct {
	Name     string
	Secret   string
	Scratch  string
	Interps  []string
	Operator *user.User
}

type server struct {
	Local
	conn net.Conn
	wmu  sync.Mutex // one frame on the wire at a time
	emu  sync.Mutex // envs, dead
	envs map[string]*env
	dead map[string]bool // an environment ended since its last run; the next run says so
}

// Serve answers one harness until the connection ends, then kills every
// environment. Each request runs in its own goroutine; runs that share an
// environment queue on it. An error frame with no id is the harness ending
// the connection on purpose, and its text is the error.
func Serve(conn net.Conn, l Local) error {
	s := &server{Local: l, conn: conn, envs: map[string]*env{}, dead: map[string]bool{}}
	if err := s.send(Frame{Header: Header{Kind: Hello, OS: runtime.GOOS, Arch: runtime.GOARCH, Interps: l.Interps, Scratch: l.Scratch, Version: version.V}}); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	for {
		f, err := Decode(br)
		if err == nil && f.Kind == Error && f.ID == "" {
			err = errors.New(string(f.Body))
		}
		if err != nil {
			s.emu.Lock()
			for _, e := range s.envs {
				e.kill()
			}
			s.emu.Unlock()
			return err
		}
		go s.handle(f)
	}
}

func (s *server) send(f Frame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return Encode(s.conn, f)
}

// handle answers one request. An error is text for the model.
func (s *server) handle(f Frame) {
	reply, err := s.answer(f)
	if err != nil {
		reply = Frame{Header: Header{Kind: Error}, Body: []byte(err.Error())}
	}
	reply.ID = f.ID
	_ = s.send(reply) // a failed send means the connection is gone; Serve sees it
}

func (s *server) answer(f Frame) (Frame, error) {
	switch f.Kind {
	case Run:
		return s.run(f)
	case Read:
		b, err := s.read(f.Path)
		return Frame{Header: Header{Kind: Bytes}, Body: b}, err
	case Write:
		w, err := s.create(f.Path)
		if err != nil {
			return Frame{}, err
		}
		_, werr := w.Write(f.Body)
		if err := w.Close(); err != nil {
			return Frame{}, err
		}
		return Frame{Header: Header{Kind: Done}}, werr
	case Kill:
		s.kill(f.Session)
		return Frame{Header: Header{Kind: Done}}, nil
	case Update:
		exe, err := update(f.Version)
		if err != nil {
			return Frame{}, err
		}
		_ = s.send(Frame{Header: Header{ID: f.ID, Kind: Done}})
		restart(exe)
	}
	return Frame{}, fmt.Errorf("unknown request %q", f.Kind)
}

func (s *server) run(f Frame) (Frame, error) {
	name := f.Interp
	if name == "" {
		name = s.Interps[0]
	}
	if !slices.Contains(s.Interps, name) {
		return Frame{}, fmt.Errorf("unknown interpreter %q, this worker runs: %s", name, strings.Join(s.Interps, ", "))
	}
	key := f.Session + "/" + name
	e, reset, err := s.acquire(key, name)
	if err != nil {
		return Frame{}, err
	}
	sp := &spill{path: filepath.Join(s.Scratch, "output", f.ID), create: s.create}
	emit := func(b []byte) error {
		if err := sp.add(b); err != nil {
			return err
		}
		return s.send(Frame{Header: Header{ID: f.ID, Kind: Output}, Body: b})
	}
	if reset {
		if err := emit([]byte("environment was reset\n")); err != nil {
			return Frame{}, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(f.Timeout)*time.Second)
	defer cancel()
	exit, alive, err := e.run(ctx, string(f.Body), "mp-done-"+f.ID, emit)
	if err != nil && alive {
		// output could not be delivered; what the process still holds is unread
		e.kill()
		alive = false
	}
	s.release(key, e, alive)
	full, cerr := sp.close()
	if ctx.Err() != nil {
		return Frame{}, fmt.Errorf("timed out after %ds, environment was reset", f.Timeout)
	}
	if err != nil {
		return Frame{}, err
	}
	if cerr != nil {
		return Frame{}, cerr
	}
	return Frame{Header: Header{Kind: Result, Exit: exit, Full: full}}, nil
}

// acquire finds or starts the environment for key and holds it for one run.
// An environment that ended while this run waited for it is not used: the
// loop finds its replacement and the reset flag its end left.
func (s *server) acquire(key, name string) (*env, bool, error) {
	it, err := s.interp(name)
	if err != nil {
		return nil, false, err
	}
	for {
		s.emu.Lock()
		e, ok := s.envs[key]
		if !ok {
			if e, err = start(it, s.Operator); err != nil {
				s.emu.Unlock()
				return nil, false, err
			}
			s.envs[key] = e
		}
		reset := s.dead[key]
		delete(s.dead, key)
		s.emu.Unlock()
		e.mu.Lock()
		s.emu.Lock()
		current := s.envs[key] == e
		s.emu.Unlock()
		if current {
			return e, reset, nil
		}
		e.mu.Unlock()
	}
}

// release hands the environment back. A dead one is forgotten so the next run
// starts fresh and says so.
func (s *server) release(key string, e *env, alive bool) {
	s.emu.Lock()
	if !alive {
		delete(s.envs, key)
		s.dead[key] = true
	}
	s.emu.Unlock()
	e.mu.Unlock()
}

// kill ends every environment of a session. The harness decides when a
// session's environments stop being worth keeping; the worker only obeys.
func (s *server) kill(session string) {
	s.emu.Lock()
	defer s.emu.Unlock()
	for key, e := range s.envs {
		if strings.HasPrefix(key, session+"/") {
			e.kill()
			delete(s.envs, key)
			s.dead[key] = true
		}
	}
}

// spill holds a run's output in memory up to the limits and, once over, writes
// all of it to a file the result names. Lines are counted as the harness
// counts them, so the file exists exactly when the harness cuts.
type spill struct {
	path   string
	create func(string) (io.WriteCloser, error)
	held   []byte
	f      io.WriteCloser
}

func (s *spill) add(b []byte) error {
	if s.f == nil {
		s.held = append(s.held, b...)
		nl := []byte("\n")
		if len(s.held) <= MaxBytes && bytes.Count(bytes.TrimSuffix(s.held, nl), nl) < MaxLines {
			return nil
		}
		f, err := s.create(s.path)
		if err != nil {
			return err
		}
		s.f, b, s.held = f, s.held, nil
	}
	_, err := s.f.Write(b)
	return err
}

// close returns the file's path, or nothing when the output stayed in memory.
// A file that could not be finished is an error, not a path to half of it.
func (s *spill) close() (string, error) {
	if s.f == nil {
		return "", nil
	}
	return s.path, s.f.Close()
}
