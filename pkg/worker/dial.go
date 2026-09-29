package worker

import (
	"errors"
	"log"
	"net"
	"net/netip"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/mesh"
)

// Redial waits grow from a blink, so a harness restart costs no visible time,
// to five seconds: a harness is restarted often while it is developed, and a
// worker that takes a minute to come back reads as a failure. A dial a second
// against a dead harness costs nothing. A dial waits longer than the longest
// wait: a first connect over a relayed path can take seconds.
// A keepalive probe every 5s idle meets a restarted harness's reset within
// seconds; a harness that is gone takes the OS's count of probes.
const (
	redialMin   = 200 * time.Millisecond
	redialMax   = 5 * time.Second
	dialTimeout = 10 * time.Second
	keepAlive   = 5 * time.Second
)

// Default is the interpreter each OS ships with. The first is the default.
var Default = map[string][]string{"windows": {"pwsh"}, "linux": {"bash"}, "darwin": {"bash"}}

// Dial makes this machine a worker of the harness on m: dial the harness's
// node at the address the last map gives it, serve until the connection
// ends, dial again, until the harness removes this worker. WireGuard proved
// both ends, so the hello carries no secret. A connection that ends within
// the longest wait, as a refused hello does, keeps the backoff; only one that
// held resets it.
func Dial(m *mesh.Mesh, l Local) {
	wait, d := redialMin, net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive}
	for {
		err := errors.New("no harness in the mesh map yet")
		if a, ok := m.Peer(Harness); ok {
			var conn net.Conn
			at := netip.AddrPortFrom(a, Port).String()
			if conn, err = d.Dial("tcp", at); err == nil {
				log.Printf("connected to the harness at %s as %s", at, l.Name)
				start, done := time.Now(), make(chan struct{})
				go hangUpOnMove(m, conn, done)
				err = Serve(conn, l)
				close(done)
				_ = conn.Close()
				if time.Since(start) > redialMax {
					wait = redialMin
				}
			}
		}
		log.Printf("%v, redial in %s", err, wait)
		select {
		case <-m.Removed():
			log.Print("removed from the mesh: no redial")
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, redialMax)
	}
}

// hangUpOnMove closes conn once this node's address is no longer the one
// conn is from, until done. A harness that lost this node registers it again
// at a new address, and Linux and macOS keep a connection from the old one
// open for minutes: its keepalives go nowhere and no reset comes back.
func hangUpOnMove(m *mesh.Mesh, conn net.Conn, done <-chan struct{}) {
	from := conn.LocalAddr().(*net.TCPAddr).AddrPort().Addr()
	for {
		select {
		case <-done:
			return
		case <-time.After(keepAlive):
		}
		if a := m.Self(); a.IsValid() && a != from {
			log.Printf("this node moved from %s to %s: hanging up", from, a)
			_ = conn.Close()
			return
		}
	}
}
