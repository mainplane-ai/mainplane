// Package mesh puts this worker on its project's Mainplane Network:
// tailscale's engine (wireguard-go, magicsock, the DERP client) on a TUN of
// its own, with its own state, UDP port, and router, so it runs beside a
// user's own Tailscale. The coordinator and relay are the harness's, reached
// over WebSocket at its front door URL, which the harness key proves.
package mesh

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tailscale.com/control/controlclient"
	"tailscale.com/envknob"
	_ "tailscale.com/feature/condregister/portmapper" // UPnP and NAT-PMP make direct paths likelier, as in tailscaled
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnauth"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store"
	"tailscale.com/net/tsdial"
	"tailscale.com/net/tstun"
	"tailscale.com/safesocket"
	"tailscale.com/tailcfg"
	"tailscale.com/tsd"
	"tailscale.com/types/key"
	"tailscale.com/types/logid"
	"tailscale.com/wgengine"
	"tailscale.com/wgengine/router"

	"github.com/mainplane-ai/mainplane/pkg/pointer"
)

const (
	// Tailscale's is 41641. The next one keeps a user's Tailscale and this
	// mesh apart and is easy to name in a firewall.
	port = 41642

	// A worker whose map poll is down proves the harness this often. A failed
	// pointer lookup is tried again after lookupWait, doubled each time up to
	// maxLookupWait: a new worker finds a harness that just started in seconds,
	// and a harness that is down draws at most a lookup a minute from each.
	check         = 20 * time.Second
	lookupWait    = 5 * time.Second
	maxLookupWait = time.Minute

	// A logout is one request to the harness. One that is down does not
	// answer in this long, and the stop it is part of goes on without it.
	logoutWait = 5 * time.Second
)

// Prefix holds every node's address, prefix::N. It is one random ULA /48, so
// it meets no LAN and not Tailscale's 100.64/10 or fd7a:115c:a1e0::/48.
var Prefix = netip.MustParsePrefix("fd7c:9a2e:4b10::/48")

// Mesh is this worker's node.
type Mesh struct {
	lb      *ipnlocal.LocalBackend
	health  *health.Tracker
	unwatch context.CancelFunc
	watched chan struct{}
	follows chan struct{}
	left    sync.Once
	err     error // leaving's
	removed chan struct{}
	sock    net.Listener // mainplane status asks here
	harness string       // the key of the harness this node follows
	report  atomic.Pointer[func() string]
}

// Report adds what f says to the end of mainplane status.
func (m *Mesh) Report(f func() string) { m.report.Store(&f) }

// Up brings the node up with its state in dir: the TUN, then the engine.
// It registers as name with the join secret on first start; the keys in
// dir register again with none. It follows the harness with key harness
// through the pointer and moves its control client when the harness moves.
func Up(dir, harness, secret, name string) (*Mesh, error) {
	// Cloudflare answers 400 to the native upgrades of both.
	envknob.Setenv("TS_CONTROL_WEBSOCKET", "1")
	envknob.Setenv("TS_DEBUG_DERP_WS_CLIENT", "1")
	controlclient.VerifyServerKey = func(ctx context.Context, u string, k key.MachinePublic) error {
		return pointer.ProveNoise(ctx, harness, u, k.String())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	logf := func(format string, a ...any) {
		// tailscale's verbose levels, and the fragmenter's probes
		if !strings.Contains(format, "[v") && !strings.HasPrefix(format, "ping(") {
			log.Printf("mesh: "+format, a...)
		}
	}
	dev, tun, err := tstun.New(logf, tunName)
	if err != nil {
		return nil, err
	}
	// The TUN is read from inside the engine, before eng is set.
	var eng wgengine.Engine
	engUp := make(chan struct{})
	frag := newFragmenter(dev, func(ip netip.Addr) bool {
		<-engUp
		pong := make(chan *ipnstate.PingResult, 1)
		eng.Ping(ip, tailcfg.PingDisco, probeSize, func(r *ipnstate.PingResult) { pong <- r })
		select {
		case r := <-pong:
			// A pong through the relay counts: the relay carries any size,
			// and cutting packets for it halves its speed too. A ping goes
			// on both while a direct path is not yet trusted; the next one,
			// on that path alone, corrects the answer.
			return r.Err == ""
		case <-time.After(probeWait):
			return false
		}
	})
	sys := tsd.NewSystem()
	dialer := &tsdial.Dialer{Logf: logf}
	dialer.SetBus(sys.Bus.Get())
	sys.Set(dialer)
	eng, err = wgengine.NewUserspaceEngine(logf, wgengine.Config{
		Tun:           frag,
		Router:        &osRouter{tun: tun},
		ListenPort:    port,
		EventBus:      sys.Bus.Get(),
		Dialer:        dialer,
		SetSubsystem:  sys.Set,
		ControlKnobs:  sys.ControlKnobs(),
		HealthTracker: sys.HealthTracker.Get(),
		Metrics:       sys.UserMetricsRegistry(),
	})
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	close(engUp)
	sys.Set(eng)
	st, err := store.NewFileStore(logf, filepath.Join(dir, "state"))
	if err != nil {
		eng.Close()
		return nil, err
	}
	sys.Set(st)
	w := sys.Tun.Get()
	w.PreFilterPacketInboundFromWireGuard = clamped(w.PreFilterPacketInboundFromWireGuard)
	w.PostFilterPacketOutboundToWireGuard = clamped(w.PostFilterPacketOutboundToWireGuard)
	w.Start()
	lb, err := ipnlocal.NewLocalBackend(logf, logid.PublicID{}, sys, controlclient.LocalBackendStartKeyOSNeutral)
	if err != nil {
		eng.Close()
		return nil, err
	}
	lb.SetVarRoot(dir)
	log.Printf("mesh: %s up, port %d", tun, port)
	ctx, unwatch := context.WithCancel(context.Background())
	m := &Mesh{lb: lb, health: sys.HealthTracker.Get(), unwatch: unwatch, watched: make(chan struct{}), follows: make(chan struct{}), removed: make(chan struct{}), harness: harness}
	go func() {
		defer close(m.watched)
		// Any change may be to a name: the block is made again from the
		// whole map and written when it differs. The coordinator marks a
		// removed node as no longer authorized, and nothing else does.
		removed := false
		lb.WatchNotifications(ctx, ipn.NotifyInitialNetMap|ipn.NotifyPeerChanges, nil, func(n *ipn.Notify) bool {
			if removed = n.State != nil && *n.State == ipn.NeedsMachineAuth; removed {
				return false
			}
			if err := hosts(block(lb.NetMapWithPeers())); err != nil {
				log.Printf("mesh: hosts: %v", err)
			}
			return true
		})
		if removed {
			log.Printf("mesh: the harness removed this worker; leaving the mesh. To join again: mainplane uninstall, then install with a join token")
			unwatch()
			if err := m.leave(); err != nil {
				log.Printf("mesh: %v", err)
			}
			close(m.removed)
		}
	}()
	go func() {
		defer close(m.follows)
		follow(ctx, lb, m.health, harness, secret, name)
	}()
	// A second worker on the machine finds the socket taken and goes without.
	if m.sock, err = safesocket.Listen(socket); err != nil {
		log.Printf("mesh: status socket: %v", err)
	} else {
		go m.serve()
	}
	return m, nil
}

// Removed is closed once the harness has removed this worker and it has
// left the mesh.
func (m *Mesh) Removed() <-chan struct{} { return m.removed }

// Self is this node's address in the last map.
func (m *Mesh) Self() netip.Addr {
	if nm := m.lb.NetMapWithPeers(); nm != nil && nm.GetAddresses().Len() > 0 {
		return nm.GetAddresses().At(0).Addr()
	}
	return netip.Addr{}
}

// Peer is the address of the peer named name in the last map, which stays
// while the coordinator is out of reach.
func (m *Mesh) Peer(name string) (netip.Addr, bool) {
	if nm := m.lb.NetMapWithPeers(); nm != nil {
		for _, p := range nm.Peers {
			if p.Name() == name && p.Addresses().Len() > 0 {
				return p.Addresses().At(0).Addr(), true
			}
		}
	}
	return netip.Addr{}, false
}

// Close leaves the mesh: the status socket, the hosts block go, the router
// takes its address, route and rule away, and the TUN goes.
func (m *Mesh) Close() error {
	if m.sock != nil {
		_ = m.sock.Close()
	}
	m.unwatch()
	<-m.watched
	return m.leave()
}

// Logout deletes this node from the harness's registry, so the name is free
// for the next node from this machine, which has new keys. It waits for
// follow to end first, which would register the node again; Close follows.
func (m *Mesh) Logout() error {
	m.unwatch()
	<-m.follows
	ctx, cancel := context.WithTimeout(context.Background(), logoutWait)
	defer cancel()
	return m.lb.Logout(ctx, ipnauth.Self)
}

// leave shuts the node down, once, and removes the hosts block.
func (m *Mesh) leave() error {
	m.left.Do(func() {
		m.lb.Shutdown()
		m.err = hosts("")
	})
	return m.err
}

// Clean removes what a killed worker left: the hosts block, a status socket
// no worker answers (off Windows, where a named pipe goes with its process),
// and on Linux and Windows the rule. Uninstall runs it after the worker stops.
func Clean() error {
	dropRule()
	var err error
	if runtime.GOOS != "windows" {
		if c, derr := net.Dial("unix", socket); derr == nil {
			_ = c.Close() // a second worker's, which serves it
		} else if rerr := os.Remove(socket); !os.IsNotExist(rerr) {
			err = rerr
		}
	}
	return errors.Join(err, hosts(""))
}

// osRouter is the router tailscale's engine drives in place of its own,
// which on Linux takes table 52, a packet mark and iptables chains that
// tailscaled already holds. It gives the TUN this node's address as a /128
// and routes the address's /48, the project's, to it. Every other route the
// engine asks for is ignored: no peer has more than its address.
type osRouter struct {
	tun  string
	addr netip.Prefix
}

func (r *osRouter) Set(c *router.Config) error {
	var a netip.Prefix
	if c != nil {
		for _, p := range c.LocalAddrs {
			if p.Addr().Is6() {
				a = p
			}
		}
	}
	if a == r.addr {
		return nil
	}
	if r.addr.IsValid() {
		if err := del(r.tun, r.addr); err != nil {
			return err
		}
		r.addr = netip.Prefix{}
	}
	if !a.IsValid() {
		return nil
	}
	if err := add(r.tun, a); err != nil {
		return err
	}
	r.addr = a
	return nil
}

func (r *osRouter) Close() error { return r.Set(nil) }

// project is the /48 of address a.
func project(a netip.Prefix) netip.Prefix { return netip.PrefixFrom(a.Addr(), 48).Masked() }

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// follow starts the control client at the harness's URL, and again at each
// URL it moves to, until ctx ends. Nothing in tailscale follows a
// coordinator that moves. A harness that still proves its URL while the map
// poll is down has likely lost this node, as when its registry was lost, and
// answers every poll with 404: the client starts again, which registers,
// with the join secret when the keys are unknown.
func follow(ctx context.Context, lb *ipnlocal.LocalBackend, h *health.Tracker, harness, secret, name string) {
	var url string
	var wait, backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = check
		if h.GetInPollNetMap() {
			continue
		}
		u := url
		if u == "" || pointer.Prove(ctx, harness, u) != nil {
			var err error
			if u, err = pointer.Find(ctx, harness, ""); err != nil {
				backoff = min(max(2*backoff, lookupWait), maxLookupWait)
				wait = backoff
				log.Printf("mesh: %v, the pointer is asked again in %s", err, wait)
				continue
			}
			backoff = 0
		}
		p := ipn.NewPrefs()
		p.ControlURL, p.Hostname, p.WantRunning = u, name, true
		p.AutoUpdate.Check = false
		err := lb.Start(ipn.Options{UpdatePrefs: p, AuthKey: secret})
		// A node with no keys yet registers with the secret, as tsnet does.
		if err == nil && lb.State() == ipn.NeedsLogin {
			err = lb.StartLoginInteractive(context.Background())
		}
		if err != nil {
			log.Printf("mesh: start at %s: %v", u, err)
			continue
		}
		url = u
		log.Printf("mesh: coordinator at %s", url)
	}
}
