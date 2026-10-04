package harness

import (
	"errors"
	"log"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// Pool is the set of workers connected right now, keyed by the name the
// coordinator gave each one's node. Workers dial in over the mesh and are
// known the moment they say hello; a lost connection forgets the worker until
// it dials again.
type Pool struct {
	mu      sync.Mutex
	m       map[string]*Remote
	refused map[string]Listed // admitted workers the last connection of which was refused

	smu     sync.Mutex                  // one Resend at a time, so an older one never lands last
	desired func(string) worker.Desired // the drives of the worker by name
}

// Listed is a worker as GET /workers shows it: its hello, and why its last
// connection was refused, when it was.
type Listed struct {
	worker.Header
	Refused string `json:"refused,omitempty"`
}

func NewPool(desired func(string) worker.Desired) *Pool {
	return &Pool{m: map[string]*Remote{}, refused: map[string]Listed{}, desired: desired}
}

// Resend sends each connected worker its drives when they changed: after
// the config, or the workers on the mesh, did.
func (p *Pool) Resend() {
	p.smu.Lock()
	defer p.smu.Unlock()
	p.mu.Lock()
	rs := slices.Collect(maps.Values(p.m))
	p.mu.Unlock()
	for _, r := range rs {
		_ = r.desire(p.desired(r.Name)) // a failed send is the connection gone; serve sees it
	}
}

func (p *Pool) Add(r *Remote) {
	p.mu.Lock()
	p.m[r.Name] = r
	delete(p.refused, r.Name)
	p.mu.Unlock()
}

// Drop hangs up on the worker named name, when it is connected.
func (p *Pool) Drop(name string) {
	p.mu.Lock()
	r, ok := p.m[name]
	p.mu.Unlock()
	if ok {
		_ = r.conn.Close()
	}
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
		l := Listed{Header: r.Header}
		l.Drives = r.Drives()
		out = append(out, l)
	}
	for name, l := range p.refused {
		if _, ok := p.m[name]; !ok {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b Listed) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Serve holds every worker that dials l, the harness's own node on the mesh,
// until l closes. WireGuard admits only nodes the coordinator gave keys to,
// and the source address is checked against node, the coordinator's
// registry, which also names the worker, so no secret rides the connection
// and no worker can take another's name. A worker that dials again under a
// name still held replaces the old entry: the old connection is dead or
// dying, and the new one is the worker as it is now.
func (p *Pool) Serve(l net.Listener, node func(netip.Addr) (string, bool)) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go p.serve(c, node)
	}
}

// serve holds one worker from hello to hangup. A refused hello is told why
// before the close, so the worker's log says it and not just EOF. A refused
// worker that passed admit is listed with the reason until it connects. The
// source is read after the hello: netstack knows it once the handshake ends.
func (p *Pool) serve(conn net.Conn, node func(netip.Addr) (string, bool)) {
	who := ""
	r, err := Connect(conn, func() (string, error) {
		a, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		who = a.Addr().String()
		n, ok := node(a.Addr())
		if err != nil || !ok {
			return "", errors.New("not a node of this mesh")
		}
		return n, nil
	})
	if err != nil {
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
	log.Printf("worker %s connected from %s: %s %s %s", r.Name, who, r.OS, r.Arch, r.Version)
	p.Add(r)
	p.Resend()
	<-r.done
	p.mu.Lock()
	if p.m[r.Name] == r {
		delete(p.m, r.Name)
		log.Printf("worker %s disconnected", r.Name)
	}
	p.mu.Unlock()
	p.Resend()
}
