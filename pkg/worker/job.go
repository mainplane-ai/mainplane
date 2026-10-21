package worker

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"
)

// jobs are the background jobs running on this worker, of every connection:
// a job outlives the connection that started it, and a kill on the next one
// still reaches it. stopped is set by kill, so the job answers done.
var jobs = struct {
	sync.Mutex
	m map[*env]*job
}{m: map[*env]*job{}}

type job struct {
	session string
	stopped bool
}

// job runs code as a background job: in an interpreter of its own, under no
// timeout, its output in <scratch>/jobs/<id>.log. It answers started at once,
// and the result with the log's tail when the code finishes. Then the job's
// processes are killed, so a job leaves nothing behind. The log ends with
// "[exit N]", or "[stopped]" for a job a kill ended, which answers done.
func (s *server) job(f Frame, it interp) (Frame, error) {
	path := filepath.Join(s.Scratch, "jobs", f.Job+".log")
	w, err := s.create(path)
	if err != nil {
		return Frame{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = w.Close()
		}
	}()
	// Started and registered under one lock, so a kill sees every job whose
	// process exists.
	jobs.Lock()
	e, err := start(it, s.Operator, f.Session)
	j := &job{session: f.Session}
	if err == nil {
		jobs.m[e] = j
	}
	jobs.Unlock()
	if err != nil {
		return Frame{}, err
	}
	defer func() {
		e.kill()
		_ = e.cmd.Wait() // reap it; an error is a wait run already did
		jobs.Lock()
		delete(jobs.m, e)
		jobs.Unlock()
	}()
	if err := s.send(Frame{Header: Header{ID: f.ID, Kind: Started, Pid: e.cmd.Process.Pid, Path: path}}); err != nil {
		return Frame{}, err
	}
	var tail []byte
	exit, _, err := e.run(context.Background(), string(f.Body), "mp-done-"+f.ID, func(b []byte) error {
		if tail = append(tail, b...); len(tail) > 2*MaxBytes {
			tail = bytes.Clone(tail[len(tail)-MaxBytes:])
		}
		_, err := w.Write(b)
		return err
	})
	e.kill()
	jobs.Lock()
	stopped := j.stopped
	jobs.Unlock()
	if stopped {
		_, _ = fmt.Fprintln(w, "[stopped]")
		return Frame{Header: Header{Kind: Done}}, nil
	}
	if err != nil {
		return Frame{}, err
	}
	if _, err := fmt.Fprintf(w, "[exit %d]\n", exit); err != nil {
		return Frame{}, err
	}
	closed = true
	if err := w.Close(); err != nil {
		return Frame{}, err
	}
	return Frame{Header: Header{Kind: Result, Exit: exit, Path: path}, Body: tail}, nil
}

// killJobs ends every job of a session.
func killJobs(session string) {
	jobs.Lock()
	defer jobs.Unlock()
	for e, j := range jobs.m {
		if j.session == session {
			j.stopped = true
			e.kill()
		}
	}
}
