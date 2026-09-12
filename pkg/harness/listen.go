package harness

import (
	"context"
	"net"
	"sync"
)

// Pool is the set of workers connected right now, keyed by the name each
// hello carries. Workers dial in and are known the moment they say hello;
// a lost connection forgets the worker until it dials again.
type Pool struct {
	mu sync.Mutex
	m  map[string]Worker
}

func NewPool() *Pool { return &Pool{m: map[string]Worker{}} }

func (p *Pool) Add(name string, w Worker) {
	p.mu.Lock()
	p.m[name] = w
	p.mu.Unlock()
}

func (p *Pool) Get(name string) (Worker, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.m[name]
	return w, ok
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
		go p.admit(conn)
	}
}

func (p *Pool) admit(conn net.Conn) {
	r, err := Connect(conn)
	if err != nil {
		_ = conn.Close()
		return
	}
	p.Add(r.Name, r)
	<-r.done
	p.mu.Lock()
	if p.m[r.Name] == Worker(r) {
		delete(p.m, r.Name)
	}
	p.mu.Unlock()
}
