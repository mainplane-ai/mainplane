package harness

import (
	"context"
	"log"
	"net"
	"slices"
	"strings"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// Pool is the set of workers connected right now, keyed by the name each
// hello carries. Workers dial in and are known the moment they say hello
// with a join secret admit accepts; a lost connection forgets the worker
// until it dials again.
type Pool struct {
	mu      sync.Mutex
	m       map[string]*Remote
	refused map[string]Listed // admitted workers the last connection of which was refused
	admit   func(secret string) bool
}

// Listed is a worker as GET /workers shows it: its hello, and why its last
// connection was refused, when it was.
type Listed struct {
	worker.Header
	Refused string `json:"refused,omitempty"`
}

func NewPool(admit func(secret string) bool) *Pool {
	return &Pool{m: map[string]*Remote{}, refused: map[string]Listed{}, admit: admit}
}

func (p *Pool) Add(r *Remote) {
	p.mu.Lock()
	p.m[r.Name] = r
	delete(p.refused, r.Name)
	p.mu.Unlock()
}

func (p *Pool) Get(name string) (*Remote, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.m[name]
	return r, ok
}

// List is every connected worker and every refused one not connected now,
// sorted by name.
func (p *Pool) List() []Listed {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Listed, 0, len(p.m)+len(p.refused))
	for _, r := range p.m {
		out = append(out, Listed{Header: r.Header})
	}
	for name, l := range p.refused {
		if _, ok := p.m[name]; !ok {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b Listed) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Listen accepts workers on addr until ctx ends. A worker that dials again
// under a name still held replaces the old entry: the old connection is dead
// or dying, and the new one is the worker as it is now.
func (p *Pool) Listen(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.serve(conn)
	}
}

// serve holds one worker from hello to hangup. A refused hello is told why
// before the close, so the worker's log says it and not just EOF. A refused
// worker that passed admit is listed with the reason until it connects.
func (p *Pool) serve(conn net.Conn) {
	r, err := Connect(conn, p.admit)
	if err != nil {
		who := conn.RemoteAddr().String()
		if r != nil {
			who = r.Name + " at " + who
			p.mu.Lock()
			p.refused[r.Name] = Listed{Header: r.Header, Refused: err.Error()}
			p.mu.Unlock()
		}
		log.Printf("worker %s refused: %v", who, err)
		_ = worker.Encode(conn, worker.Frame{Header: worker.Header{Kind: worker.Error}, Body: []byte(err.Error())})
		_ = conn.Close()
		return
	}
	log.Printf("worker %s connected: %s %s %s", r.Name, r.OS, r.Arch, r.Version)
	p.Add(r)
	<-r.done
	p.mu.Lock()
	if p.m[r.Name] == r {
		delete(p.m, r.Name)
		log.Printf("worker %s disconnected", r.Name)
	}
	p.mu.Unlock()
}
