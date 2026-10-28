package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// session is what the harness holds in memory about one session: a cache of
// the file, never the other way round. Every open, append, and close of the
// session's file happens under mu. A step holds the file open for its whole
// run, so a message posted mid-step appends to the same handle and returns at
// once.
type session struct {
	id      string
	mu      sync.Mutex
	f       *statefile.File // the file, while a step holds it
	changed chan struct{}   // closed and replaced on every append
	retry   bool            // a run asked to step past a failed tip
	stopped string          // who asked for the stop, while one is in flight

	// under Harness.mu
	running  bool               // a runner goroutine is stepping this session
	kicked   bool               // a kick arrived while running: look again before stopping
	cancel   context.CancelFunc // ends the running step
	done     chan struct{}      // closed when the runner goroutine ends
	deleting bool               // no kick starts or continues a runner
}

func (h *Harness) session(id string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.s == nil {
		h.s = map[string]*session{}
	}
	s, ok := h.s[id]
	if !ok {
		s = &session{id: id, changed: make(chan struct{})}
		h.s[id] = s
	}
	return s
}

// hold opens the file for a step.
func (s *session) hold(sessions statefile.Sessions, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := sessions.Open(id)
	if err != nil {
		return err
	}
	s.f = f
	return nil
}

func (s *session) release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.f.Close()
	s.f = nil
	return err
}

// write appends one record to f, wakes every waiter, and marks the session
// for the index. The caller holds s.mu.
func (h *Harness) write(s *session, f *statefile.File, r statefile.Record) (int, error) {
	if err := f.Append(r); err != nil {
		return 0, err
	}
	close(s.changed)
	s.changed = make(chan struct{})
	h.touch(s.id)
	return f.N(), nil
}

// append writes one record to the file a step holds.
func (h *Harness) append(s *session, r statefile.Record) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return h.write(s, s.f, r)
}

// Part is one body of a post: its content type and bytes.
type Part struct {
	Type string
	Body []byte
}

// Post appends one message record per part under one lock and kicks the
// session once, so a text and an image reach the model as one turn. The
// append is the acknowledgement: a closed session starts stepping on it, a
// stepping one sees it at its next context build. Returns the last n. A post
// with a key appends only the parts the file does not hold with that key, so
// a connector that lost the reply posts again and the post ends whole, once.
func (h *Harness) Post(ctx context.Context, id, via, key string, parts []Part) (int, error) {
	s := h.session(id)
	n, err := func() (int, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var done, n int
		if key != "" {
			chain, err := h.Sessions.Load(id)
			if err != nil {
				return 0, err
			}
			for _, r := range chain {
				if r.Kind == statefile.Message && r.IdempotencyKey == key {
					done, n = done+1, r.N
				}
			}
		}
		if done >= len(parts) {
			return n, nil
		}
		f := s.f
		if f == nil {
			var err error
			if f, err = h.Sessions.Open(id); err != nil {
				return 0, err
			}
			defer func() { _ = f.Close() }()
		}
		for _, p := range parts[done:] {
			var err error
			if n, err = h.write(s, f, statefile.Record{Header: statefile.Header{Kind: statefile.Message, Type: p.Type, Via: via, IdempotencyKey: key}, Body: p.Body}); err != nil {
				return 0, err
			}
		}
		return n, nil
	}()
	if err != nil {
		return 0, err
	}
	h.Kick(ctx, id)
	return n, nil
}

// SetWorkers appends a config record that is the newest one with these
// workers, under the lock a post takes. Nothing else in the config changes. A
// step running keeps the config it started with; the next step lists the new
// workers to the model as a change.
func (h *Harness) SetWorkers(id, via string, workers []statefile.Worker) (int, error) {
	s := h.session(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return 0, err
	}
	conf, err := config(chain)
	if err != nil {
		return 0, err
	}
	conf.Workers = workers
	if conf.Workers == nil {
		conf.Workers = []statefile.Worker{}
	}
	body, err := json.Marshal(conf)
	if err != nil {
		return 0, err
	}
	f := s.f
	if f == nil {
		if f, err = h.Sessions.Open(id); err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
	}
	return h.write(s, f, statefile.Record{Header: statefile.Header{Kind: statefile.Config, Type: "application/json", Via: via}, Body: body})
}

// MaxName bounds a session's name: a title a list shows on one line.
const MaxName = 100

func checkName(n string) error {
	if len(n) > MaxName || !utf8.ValidString(n) || strings.ContainsFunc(n, newline) {
		return fmt.Errorf("a name is one line of at most %d bytes of UTF-8", MaxName)
	}
	return nil
}

// newline is any rune that breaks or controls a line, U+2028 and U+2029 too.
func newline(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) }

func name(via, n string) statefile.Record {
	return statefile.Record{Header: statefile.Header{Kind: statefile.Name, Type: "text/plain", Via: via}, Body: []byte(n)}
}

// SetName appends a name record under the lock a post takes. An empty name
// removes the session's name. The model never sees it and status is unchanged.
func (h *Harness) SetName(id, via, n string) (int, error) {
	if err := checkName(n); err != nil {
		return 0, err
	}
	s := h.session(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.f
	if f == nil {
		var err error
		if f, err = h.Sessions.Open(id); err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
	}
	return h.write(s, f, name(via, n))
}

// Delete stops the session, then removes its file for good. Append-only is
// of records: none is ever changed, and a session goes whole. It waits for
// the stopped step to end, so no step writes to a file that is gone; under
// the lock a post takes, so no post does. Copies keep their own files,
// workers their logs.
func (h *Harness) Delete(ctx context.Context, id, via string) error {
	s := h.session(id)
	h.mu.Lock()
	if s.deleting {
		h.mu.Unlock()
		return fmt.Errorf("session %s is already being deleted", id)
	}
	s.deleting = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		s.deleting = false
		h.mu.Unlock()
	}()
	if err := h.Stop(ctx, id, via); err != nil {
		log.Printf("session %s: stop before delete: %v", id, err) // a broken file still goes
	}
	h.mu.Lock()
	if s.running {
		done := s.done
		h.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		h.mu.Lock()
	}

	s.mu.Lock()
	h.mu.Unlock()
	err := os.Remove(h.Sessions.Path(id))
	if err == nil {
		h.lockIndex()
		delete(h.index, id)
		delete(h.dirty, id)
		h.gone[id] = true
		h.imu.Unlock()
		close(s.changed) // long polls end, and read that it is gone
		s.changed = make(chan struct{})
	}
	s.mu.Unlock() // before h.mu: the order is h.mu then s.mu, never the reverse
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no session %s", id)
	} else if err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.s, id)
	h.mu.Unlock()
	log.Printf("session %s: deleted by %s", id, via)
	return nil
}

// Kick steps a session in the background until it is not open. A kick during
// a run makes the runner look once more before it stops, so a message that
// lands as a run ends is not left waiting. Each pass gets its own context so
// a stop ends one pass and not the next. A pass that ends closed or failed
// copies the file to the session's workers.
func (h *Harness) Kick(ctx context.Context, id string) {
	s := h.session(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.deleting {
		return
	}
	if s.running {
		s.kicked = true
		return
	}
	s.running, s.done = true, make(chan struct{})
	done := s.done
	rctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go func() {
		for {
			st, err := h.Run(rctx, id)
			cancel()
			if err != nil {
				log.Printf("session %s: %v", id, err)
			} else {
				log.Printf("session %s: %s", id, st)
			}
			s.mu.Lock()
			s.stopped = ""
			s.mu.Unlock()
			h.mu.Lock()
			if !s.kicked || s.deleting {
				s.running, s.cancel, s.kicked = false, nil, false
				h.mu.Unlock()
				h.touch(id)
				if st == statefile.StatusClosed || st == statefile.StatusFailed {
					h.copyLogs(ctx, id)
				}
				close(done)
				return
			}
			s.kicked = false
			rctx, cancel = context.WithCancel(ctx)
			s.cancel = cancel
			h.mu.Unlock()
		}
	}()
}

// Retry steps a failed session from its tip: after a stop it continues, after
// exhausted provider retries it tries again, past the limit it gets the same
// error again. Nothing is cleared.
func (h *Harness) Retry(ctx context.Context, id string) {
	s := h.session(id)
	s.mu.Lock()
	s.retry = true
	s.mu.Unlock()
	h.Kick(ctx, id)
}

func (s *session) takeRetry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.retry
	s.retry = false
	return r
}

func (s *session) stoppedBy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// Stop ends everything the session is doing. A running step is cut now: the
// provider stream is cut, every call in flight is answered "stopped, effect
// unknown", a call of a recorded step not yet started is answered "not run",
// and the last record is an error naming via; a session not stepping gets a
// system record from stop instead. Either way every environment and job the
// session has on its connected workers is killed.
func (h *Harness) Stop(ctx context.Context, id, via string) error {
	s := h.session(id)
	h.mu.Lock()
	running := s.running
	if running {
		s.mu.Lock()
		s.stopped = via
		s.mu.Unlock()
		s.cancel()
	}
	h.mu.Unlock()
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return err
	}
	if !running {
		if err := h.note(s, id, system("stop", fmt.Sprintf(idleStop, via))); err != nil {
			return err
		}
	}
	h.killAll(ctx, id, chain)
	return nil
}

// note appends r whether or not a step holds the file, as a post does.
func (h *Harness) note(s *session, id string, r statefile.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.f
	if f == nil {
		var err error
		if f, err = h.Sessions.Open(id); err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
	}
	_, err := h.write(s, f, r)
	return err
}

// killAll ends the session's environments on every connected worker any of
// its configs lists, so a worker taken out of the session loses what the
// session started there too.
func (h *Harness) killAll(ctx context.Context, id string, chain []statefile.Record) {
	seen := map[string]bool{}
	for _, r := range chain {
		if r.Kind != statefile.Config {
			continue
		}
		var conf statefile.Conf
		if err := json.Unmarshal(r.Body, &conf); err != nil {
			log.Printf("session %s: config record %d: %v", id, r.N, err)
			continue
		}
		for _, w := range conf.Workers {
			if rem, ok := h.Workers.Get(w.Name); ok && !seen[w.Name] {
				seen[w.Name] = true
				kill(ctx, rem, id)
			}
		}
	}
}

// Resume kicks every session that needs a step. This is the open-files index,
// rebuilt from the tips on start; a failed session waits for a run or a
// message. A file that does not read is logged and left out, not a reason to
// stay down.
func (h *Harness) Resume(ctx context.Context) error {
	ids, err := h.Sessions.List()
	if err != nil {
		return err
	}
	for _, id := range ids {
		info, err := h.Info(id)
		if err != nil {
			log.Printf("session %s: %v", id, err)
			continue
		}
		if info.Status == statefile.StatusOpen || info.Status == statefile.StatusInterrupted {
			h.Kick(ctx, id)
		}
	}
	go h.lostJobs(ids)
	return nil
}

// lostJobs tells each session of the jobs a harness before this one started
// and never heard the end of: their calls ended with that harness, so no
// message will come. Each gets a system record from job, keyed by its call's
// id, which opens nothing: a restart must not wake every session that once
// started a server.
func (h *Harness) lostJobs(ids []string) {
	for _, id := range ids {
		chain, err := h.Sessions.Load(id)
		if err != nil {
			log.Printf("session %s: %v", id, err)
			continue
		}
		background, told := map[string]bool{}, map[string]bool{}
		for _, r := range chain {
			var c provider.Call
			var a args
			switch {
			case r.Kind == statefile.Message && r.Via == "job" || r.Kind == statefile.System && r.Source == "job":
				told[r.IdempotencyKey] = true
			case r.Kind == statefile.Call && json.Unmarshal(r.Body, &c) == nil && c.Name == "run" && json.Unmarshal(c.Arguments, &a) == nil:
				background[r.ID] = a.Background
			case r.Kind == statefile.Error && r.Via != "" || r.Kind == statefile.System && r.Source == "stop":
				clear(background) // a stop killed every job before it
			}
		}
		for _, r := range chain {
			if r.Kind != statefile.Result || !background[r.For] || told[r.For] || !strings.HasPrefix(string(r.Body), "started job ") {
				continue
			}
			n := system("job", fmt.Sprintf("[%s: the harness restarted, so it lost contact. It may or may not still be running: check the pid and the log, whose last line is [exit N] once it ended]", r.Body))
			n.IdempotencyKey = r.For
			if err := h.note(h.session(id), id, n); err != nil {
				log.Printf("session %s: job %s: %v", id, r.For, err)
			}
		}
	}
}

// status is the file's word, or stepping while a runner holds the session.
func (h *Harness) status(id string, chain []statefile.Record) statefile.Status {
	s := h.session(id)
	h.mu.Lock()
	running := s.running
	h.mu.Unlock()
	if running {
		return statefile.StatusStepping
	}
	return statefile.Derive(chain)
}

// Changed is a channel closed at the session's next append.
func (h *Harness) Changed(id string) <-chan struct{} {
	s := h.session(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}
