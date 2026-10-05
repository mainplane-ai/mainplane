package mesh

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"
)

// socket is where the worker answers mainplane status and the CLI's question
// for its harness: a unix socket any local user may open, a named pipe on
// Windows. A client writes one line: status, joined, or harness <peer name>.
var socket = "/var/run/mainplaned.sock"

// A client that writes no line in this long is hung up on.
const askWait = 5 * time.Second

// ErrNoWorker is a socket no worker answers: none runs on this machine.
var ErrNoWorker = errors.New("this machine is not a worker")

func init() {
	if runtime.GOOS == "windows" {
		socket = `\\.\pipe\mainplaned`
	}
}

// ask writes line to the worker on this machine and returns its answer.
func ask(line string) (string, error) {
	var c io.ReadWriteCloser
	var err error
	if runtime.GOOS == "windows" {
		c, err = os.OpenFile(socket, os.O_RDWR, 0) // a named pipe opens as a file
	} else {
		c, err = net.Dial("unix", socket)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoWorker, err)
	}
	defer func() { _ = c.Close() }()
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(c)
	return string(b), err
}

// Status is what the worker on this machine says of its node.
func Status() (string, error) { return ask("status") }

// Harness is the key of the harness the worker on this machine follows, and
// the address of its peer named name, the harness's node, while the map poll
// is up. A CLI whose harness has that key reaches it there, off the tunnel.
func Harness(name string) (string, netip.Addr, error) {
	s, err := ask("harness " + name)
	if err != nil {
		return "", netip.Addr{}, err
	}
	k, a, _ := strings.Cut(strings.TrimSpace(s), " ")
	addr, err := netip.ParseAddr(a)
	return k, addr, err
}

// Joined is the key of the harness the worker on this machine follows, and
// its name there, once the harness has registered it and until it removes
// it. A worker that never joined, as with a revoked token, is an error.
func Joined() (harness, name string, err error) {
	s, err := ask("joined")
	if err != nil {
		return "", "", err
	}
	harness, name, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return "", "", errors.New("the worker on this machine is not joined to its harness")
	}
	return harness, name, nil
}

// serve answers every connection to the socket until Close.
func (m *Mesh) serve() {
	for {
		c, err := m.sock.Accept()
		if err != nil {
			return
		}
		go m.answer(c)
	}
}

// answer reads one line from c and answers it. A harness node not in the
// map, or a map poll down, is an empty answer.
func (m *Mesh) answer(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(askWait))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return
	}
	switch verb, name, _ := strings.Cut(strings.TrimSpace(line), " "); verb {
	case "status":
		_, _ = io.WriteString(c, m.status())
	case "harness":
		if a, ok := m.Peer(name); ok && m.health.GetInPollNetMap() {
			_, _ = fmt.Fprintf(c, "%s %s\n", m.harness, a)
		}
	case "joined":
		select {
		case <-m.removed:
			return
		default:
		}
		if nm := m.lb.NetMapWithPeers(); nm != nil {
			_, _ = fmt.Fprintf(c, "%s %s\n", m.harness, nm.SelfNode.Name())
		}
	}
}

// status is this node's name and address, whether its harness answers the
// map poll, and each peer's name, address, and path: direct to an endpoint,
// or through a relay region, which is slower, not broken. A peer with no
// direct path and no traffic of late is idle: the path is found when traffic
// starts.
func (m *Mesh) status() string {
	select {
	case <-m.removed:
		return "removed from the mesh by its harness\n"
	default:
	}
	self, hint := "this machine", ""
	if !m.health.GetInPollNetMap() {
		self += ", harness unresponsive"
		hint = fmt.Sprintf("the harness at %s does not answer. If it was uninstalled or installed again, run mainplane uninstall here, then its new install line\n", m.lb.Prefs().ControlURL())
	}
	nm := m.lb.NetMapWithPeers()
	if nm == nil {
		return "no map yet from the harness\n" + hint
	}
	var b strings.Builder
	st := m.lb.Status()
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", nm.SelfNode.Name(), m.Self(), self)
	for _, p := range nm.Peers {
		path := "no path yet"
		if ps := st.Peer[p.Key()]; ps != nil {
			switch {
			case ps.CurAddr != "":
				path = "direct " + ps.CurAddr
			case !ps.Active:
				path = "idle"
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
	if f := m.report.Load(); f != nil {
		b.WriteString((*f)())
	}
	if hint != "" {
		b.WriteString("\n" + hint)
	}
	return b.String()
}
