package apps

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
)

// A daemon that exits is run again after relaunch, long enough that one
// that cannot start does not spin. A call waits startWait for a daemon just
// launched to listen, about what an interpreter takes to load a server.
const (
	relaunch  = time.Second
	startWait = 10 * time.Second
)

// daemon is one app's process as the harness runs it now. Each launch has
// its own port and proxy secret; ready closes when the port answers.
type daemon struct {
	cancel context.CancelFunc
	port   int
	secret string
	ready  chan struct{}
}

// sync starts every daemon app that should run and does not: not stopped by
// an admin, and with every secret it names.
func (m *Manager) sync() error {
	secrets, err := m.secrets()
	if err != nil {
		return err
	}
	for _, name := range m.names() {
		a, err := m.load(name)
		if err != nil || a.kind() != "daemon" || len(missing(a.spec, secrets)) > 0 {
			continue
		}
		m.mu.Lock()
		if m.daemons[name] == nil && !m.stopped[name] {
			ctx, cancel := context.WithCancel(m.ctx)
			d := &daemon{cancel: cancel, ready: make(chan struct{})}
			m.daemons[name] = d
			go m.supervise(ctx, a, d, secrets)
		}
		m.mu.Unlock()
	}
	return nil
}

// enable is an admin's start or stop of a daemon app.
func (m *Manager) enable(name string, on bool) error {
	a, err := m.load(name)
	if err == nil && a.kind() != "daemon" {
		err = fmt.Errorf("app %s is not a daemon: nothing to start or stop", name)
	}
	m.mu.Lock()
	if !on && m.daemons[name] != nil {
		err = nil // it runs from the app.json it started with, and stops though the file no longer loads
	}
	if err == nil {
		m.stopped[name] = !on
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if !on {
		m.stop(name)
		return nil
	}
	secrets, err := m.secrets()
	if err != nil {
		return err
	}
	if miss := missing(a.spec, secrets); len(miss) > 0 {
		return fmt.Errorf("app %s is missing secrets: %s", name, strings.Join(miss, ", "))
	}
	return m.sync()
}

func (m *Manager) stop(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.daemons[name]; d != nil {
		d.cancel()
		delete(m.daemons, name)
	}
}

// reserved is whether name is a variable supervise sets for a daemon, which
// a secret of that name would replace. Windows reads names without case.
func reserved(name string) bool {
	n := strings.ToUpper(name)
	return n == "PATH" || n == "HOME" || n == "PORT" || n == "DATA_DIR" || strings.HasPrefix(n, "MAINPLANE_")
}

// supervise runs the app's command until ctx ends, again each time it exits.
// The process gets its port, the secret the harness proves itself with, its
// data folder, the secrets it names, and a key of its own only when it has
// entitlements. It does not get the harness's environment, where provider
// keys may be. It leads a process tree of its own, which ends whole when the
// daemon is stopped, and where the system can still find it (not Windows)
// when its first process exits.
func (m *Manager) supervise(ctx context.Context, a app, d *daemon, secrets map[string]string) {
	data := m.data(a.name)
	if err := os.MkdirAll(data, 0o755); err != nil {
		log.Printf("app %s: %v", a.name, err)
	}
	for ctx.Err() == nil {
		port, err := freePort()
		if err != nil {
			log.Printf("app %s: %v", a.name, err)
			return
		}
		m.mu.Lock()
		ready := d.ready
		d.port, d.secret = port, rand.Text()
		env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PORT=" + strconv.Itoa(port), "MAINPLANE_PROXY_SECRET=" + d.secret, "DATA_DIR=" + data, "MAINPLANE_URL=" + m.url}
		m.mu.Unlock()
		for _, n := range a.spec.Secrets {
			env = append(env, n+"="+secrets[n])
		}
		if len(a.spec.Entitlements) > 0 {
			env = append(env, "MAINPLANE_APP_KEY="+auth.Sign(m.k, auth.App, m.key, a.name))
		}
		cmd := exec.CommandContext(ctx, a.spec.Run[0], a.spec.Run[1:]...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = a.dir, env, os.Stderr, os.Stderr, relaunch
		cmd.Cancel = func() error { return killTree(cmd.Process) }
		own(cmd)
		lctx, launched := context.WithCancel(ctx) // a process that dies before it listens ends its poll
		go listening(lctx, port, ready)
		log.Printf("app %s: %v on port %d", a.name, a.spec.Run, port)
		err = cmd.Run()
		launched()
		if cmd.Process != nil {
			sweep(cmd.Process) // what it started and left behind would pile up, one per launch
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("app %s: exited: %v", a.name, err)
		m.mu.Lock()
		d.ready = make(chan struct{})
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(relaunch):
		}
	}
}

// listening closes ready once the port takes a connection.
func listening(ctx context.Context, port int, ready chan struct{}) {
	for ctx.Err() == nil {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), relaunch); err == nil {
			_ = c.Close()
			close(ready)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// running is the port and proxy secret of the app's daemon once it listens.
func (m *Manager) running(ctx context.Context, name string) (int, string, error) {
	m.mu.Lock()
	d := m.daemons[name]
	m.mu.Unlock()
	if d == nil {
		return 0, "", errors.New("app " + name + " is not running")
	}
	m.mu.Lock()
	ready := d.ready
	m.mu.Unlock()
	select {
	case <-ready:
		m.mu.Lock()
		defer m.mu.Unlock()
		return d.port, d.secret, nil
	case <-ctx.Done():
		return 0, "", ctx.Err()
	case <-time.After(startWait):
		return 0, "", errors.New("app " + name + " is not listening on its $PORT")
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
