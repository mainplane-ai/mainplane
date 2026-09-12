package harness

import (
	"context"
	"log"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// session is what the harness holds in memory about one session: a cache of
// the file, never the other way round. Every open, append, close, and link of
// the session's files happens under mu. A step holds the live file open for
// its whole run, so a message posted mid-step appends to the same handle and
// returns at once.
type session struct {
	mu      sync.Mutex
	f       *statefile.File // the live file, while a step holds it
	changed chan struct{}   // closed and replaced on every append

	running bool // a runner goroutine is stepping this session
	kicked  bool // a kick arrived while running: look again before stopping
}

func (h *Harness) session(id string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.s == nil {
		h.s = map[string]*session{}
	}
	s, ok := h.s[id]
	if !ok {
		s = &session{changed: make(chan struct{})}
		h.s[id] = s
	}
	return s
}

// hold opens the live file for a step.
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

// write appends one record to f and wakes every waiter. The caller holds mu.
func (s *session) write(f *statefile.File, r statefile.Record) (statefile.Position, error) {
	if err := f.Append(r); err != nil {
		return statefile.Position{}, err
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return statefile.Position{File: f.Num, N: f.N()}, nil
}

// append writes one record to the file a step holds.
func (s *session) append(r statefile.Record) (statefile.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(s.f, r)
}

// Post appends a message from a connector and kicks the session. The append
// is the acknowledgement: a closed session starts stepping on it, a stepping
// one sees it at its next context build.
func (h *Harness) Post(ctx context.Context, id, via, ctype string, body []byte) (statefile.Position, error) {
	s := h.session(id)
	p, err := func() (statefile.Position, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		f := s.f
		if f == nil {
			var err error
			if f, err = h.Sessions.Open(id); err != nil {
				return statefile.Position{}, err
			}
			defer func() { _ = f.Close() }()
		}
		return s.write(f, statefile.Record{Header: statefile.Header{Kind: statefile.Message, Type: ctype, Via: via}, Body: body})
	}()
	if err != nil {
		return p, err
	}
	p.Session = id
	h.Kick(ctx, id)
	return p, nil
}

// Kick steps a session in the background until it is not open. A kick during
// a run makes the runner look once more before it stops, so a message that
// lands as a run ends is not left waiting.
func (h *Harness) Kick(ctx context.Context, id string) {
	s := h.session(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.running {
		s.kicked = true
		return
	}
	s.running = true
	go func() {
		for {
			if st, err := h.Run(ctx, id); err != nil {
				log.Printf("session %s: %v", id, err)
			} else {
				log.Printf("session %s: %s", id, st)
			}
			h.mu.Lock()
			if !s.kicked {
				s.running = false
				h.mu.Unlock()
				return
			}
			s.kicked = false
			h.mu.Unlock()
		}
	}()
}

// Resume kicks every session that is not closed. This is the open-files index,
// rebuilt from the tips on start.
func (h *Harness) Resume(ctx context.Context) error {
	ids, err := h.Sessions.List()
	if err != nil {
		return err
	}
	for _, id := range ids {
		chain, err := h.Sessions.Load(id)
		if err != nil {
			return err
		}
		if statefile.Derive(chain) != statefile.StatusClosed {
			h.Kick(ctx, id)
		}
	}
	return nil
}

// Status is the file's word, or stepping while a runner holds the session.
func (h *Harness) Status(id string) (statefile.Status, int, error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return "", 0, err
	}
	s := h.session(id)
	h.mu.Lock()
	running := s.running
	h.mu.Unlock()
	if running {
		return statefile.StatusStepping, chain[len(chain)-1].File, nil
	}
	return statefile.Derive(chain), chain[len(chain)-1].File, nil
}

// Wait returns when the session's file changes or ctx ends.
func (h *Harness) Wait(ctx context.Context, id string) {
	s := h.session(id)
	s.mu.Lock()
	ch := s.changed
	s.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}
