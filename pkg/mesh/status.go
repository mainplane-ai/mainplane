package mesh

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"tailscale.com/safesocket"
)

// socket is where the worker answers mainplane status: a unix socket any
// local user may open, a named pipe on Windows.
var socket = "/var/run/mainplaned.sock"

func init() {
	if runtime.GOOS == "windows" {
		socket = `\\.\pipe\mainplaned`
	}
}

// Status is what the worker on this machine says of its node.
func Status() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := safesocket.ConnectContext(ctx, socket)
	if err != nil {
		return "", fmt.Errorf("no worker runs on this machine: %w", err)
	}
	defer func() { _ = c.Close() }()
	b, err := io.ReadAll(c)
	return string(b), err
}

// serve answers every connection to the socket with the status, for as
// long as the process runs. A second worker on the machine finds the socket
// taken and goes without.
func (m *Mesh) serve() {
	l, err := safesocket.Listen(socket)
	if err != nil {
		log.Printf("mesh: status socket: %v", err)
		return
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_, _ = io.WriteString(c, m.status())
		_ = c.Close()
	}
}

// status is this node's name and address, its coordinator, and each peer's
// name, address, and path: direct to an endpoint, or through a relay
// region, which is slower, not broken.
func (m *Mesh) status() string {
	select {
	case <-m.removed:
		return "removed from the mesh by its harness\n"
	default:
	}
	nm := m.lb.NetMapWithPeers()
	if nm == nil {
		return fmt.Sprintf("no map yet from the coordinator at %s\n", m.lb.Prefs().ControlURL())
	}
	poll := "map poll down"
	if m.health.GetInPollNetMap() {
		poll = "map poll up"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  coordinator %s, %s\n\n", nm.SelfNode.Name(), m.Self(), m.lb.Prefs().ControlURL(), poll)
	st := m.lb.Status()
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, p := range nm.Peers {
		path := "no path yet"
		if ps := st.Peer[p.Key()]; ps != nil {
			switch {
			case ps.CurAddr != "":
				path = "direct " + ps.CurAddr
			case ps.Relay != "":
				path = "relay " + ps.Relay
			}
			if !ps.Online {
				path += ", offline"
			}
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name(), p.Addresses().At(0).Addr(), path)
	}
	_ = w.Flush()
	return b.String()
}
