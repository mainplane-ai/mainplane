package harness

import (
	"context"
	"fmt"
	"log"
	"sync"

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
	running bool               // a runner goroutine is stepping this session
	kicked  bool               // a kick arrived while running: look again before stopping
	cancel  context.CancelFunc // ends the running step
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

// Kick steps a session in the background until it is not open. A kick during
// a run makes the runner look once more before it stops, so a message that
// lands as a run ends is not left waiting. Each pass gets its own context so
// a stop ends one pass and not the next. A pass that ends closed or failed
// copies the file to the session's workers.
func (h *Harness) Kick(ctx context.Context, id string) {
	s := h.session(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.running {
		s.kicked = true
		return
	}
	s.running = true
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
			if !s.kicked {
				s.running, s.cancel = false, nil
				h.mu.Unlock()
				h.touch(id)
				if st == statefile.StatusClosed || st == statefile.StatusFailed {
					h.copyLogs(ctx, id)
				}
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

// Stop ends the running step now: the provider stream is cut, every call in
// flight is killed on its worker and answered "stopped, effect unknown", and
// the last record is an error naming via. A session that is not stepping has
// nothing to stop.
func (h *Harness) Stop(ctx context.Context, id, via string) error {
	s := h.session(id)
	h.mu.Lock()
	if !s.running {
		h.mu.Unlock()
		return fmt.Errorf("session %s is not stepping", id)
	}
	s.mu.Lock()
	s.stopped = via
	s.mu.Unlock()
	cancel := s.cancel
	h.mu.Unlock()
	cancel()
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return err
	}
	conf, err := config(chain)
	if err != nil {
		return err
	}
	for _, w := range conf.Workers {
		if r, ok := h.Workers.Get(w.Name); ok {
			if err := r.Kill(ctx, id); err != nil {
				log.Printf("session %s: kill on %s: %v", id, w.Name, err)
			}
		}
	}
	return nil
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
	return nil
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
