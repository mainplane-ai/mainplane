// Package mesh puts this worker on its project's Mainplane Network:
// tailscale's engine (wireguard-go, magicsock, the DERP client) on a TUN of
// its own, with its own state, UDP port, and router, so it runs beside a
// user's own Tailscale. The coordinator and relay are the harness's, reached
// over WebSocket at its front door URL, which the harness key proves.
package mesh

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/control/controlclient"
	"tailscale.com/envknob"
	_ "tailscale.com/feature/condregister/portmapper" // UPnP and NAT-PMP make direct paths likelier, as in tailscaled
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/ipn/store"
	"tailscale.com/net/tsdial"
	"tailscale.com/net/tstun"
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

	// A worker whose map poll is down proves the harness this often, and
	// asks the pointer at most once a minute: a harness that is down would
	// otherwise draw a lookup from every worker.
	check      = 20 * time.Second
	lookupWait = time.Minute
)

// Mesh is this worker's node.
type Mesh struct {
	lb      *ipnlocal.LocalBackend
	unwatch context.CancelFunc
	watched chan struct{}
	left    sync.Once
	err     error // leaving's
	removed chan struct{}
}

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
		if !strings.Contains(format, "[v") { // tailscale's verbose levels
			log.Printf("mesh: "+format, a...)
		}
	}
	dev, tun, err := tstun.New(logf, tunName)
	if err != nil {
		return nil, err
	}
	sys := tsd.NewSystem()
	dialer := &tsdial.Dialer{Logf: logf}
	dialer.SetBus(sys.Bus.Get())
	sys.Set(dialer)
	eng, err := wgengine.NewUserspaceEngine(logf, wgengine.Config{
		Tun:           dev,
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
	sys.Set(eng)
	st, err := store.NewFileStore(logf, filepath.Join(dir, "state"))
	if err != nil {
		eng.Close()
		return nil, err
	}
	sys.Set(st)
	sys.Tun.Get().Start()
	lb, err := ipnlocal.NewLocalBackend(logf, logid.PublicID{}, sys, controlclient.LocalBackendStartKeyOSNeutral)
	if err != nil {
		eng.Close()
		return nil, err
	}
	lb.SetVarRoot(dir)
	log.Printf("mesh: %s up, port %d", tun, port)
	ctx, unwatch := context.WithCancel(context.Background())
	m := &Mesh{lb: lb, unwatch: unwatch, watched: make(chan struct{}), removed: make(chan struct{})}
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
	go follow(ctx, lb, sys.HealthTracker.Get(), harness, secret, name)
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

// Close leaves the mesh: the hosts block goes, the router takes its address,
// route and rule away, and the TUN goes.
func (m *Mesh) Close() error {
	m.unwatch()
	<-m.watched
	return m.leave()
}

// leave shuts the node down, once, and removes the hosts block.
func (m *Mesh) leave() error {
	m.left.Do(func() {
		m.lb.Shutdown()
		m.err = hosts("")
	})
	return m.err
}

// Clean removes what a killed worker left: the hosts block, and on Linux
// and Windows the rule. Uninstall runs it after the worker stops.
func Clean() error {
	dropRule()
	return hosts("")
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
	url, asked := "", time.Time{}
	for ; ctx.Err() == nil; time.Sleep(check) {
		if h.GetInPollNetMap() {
			continue
		}
		u := url
		if u == "" || pointer.Prove(ctx, harness, u) != nil {
			if time.Since(asked) < lookupWait {
				continue
			}
			asked = time.Now()
			var err error
			if u, err = pointer.Find(ctx, harness, ""); err != nil {
				log.Printf("mesh: %v, the pointer is asked again within a minute", err)
				continue
			}
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
