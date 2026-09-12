package harness

import (
	"context"
	"log"
	"net"
	"sync"
)

// Pool is the set of workers connected right now, keyed by the name each
// hello carries. Workers dial in and are known the moment they say hello;
// a lost connection forgets the worker until it dials again.
type Pool struct {
	mu sync.Mutex
	m  map[string]*Remote
}

func NewPool() *Pool { return &Pool{m: map[string]*Remote{}} }

func (p *Pool) Add(r *Remote) {
	p.mu.Lock()
	p.m[r.Name] = r
	p.mu.Unlock()
}

func (p *Pool) Get(name string) (*Remote, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.m[name]
	return r, ok
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
	log.Printf("worker %s connected: %s %s", r.Name, r.OS, r.Arch)
	p.Add(r)
	<-r.done
	log.Printf("worker %s disconnected", r.Name)
	p.mu.Lock()
	if p.m[r.Name] == r {
		delete(p.m, r.Name)
	}
	p.mu.Unlock()
}
