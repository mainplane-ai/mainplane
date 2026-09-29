// Package coordinator is the harness's side of the Tailscale coordination
// protocol: ts2021, Noise inside a WebSocket at /ts2021, the one upgrade the
// tunnel passes. A worker registers with its join secret and gets an address
// in the project's /48 and a name. The harness's own node registers the same
// way, on loopback, with a secret only this process holds. Then each node
// long-polls a map of the nodes it may reach and of the relay. The nodes are
// in nodes.json in the admin dir.
//
// The protocol handling is copied from headscale's hscontrol (see LICENSE)
// and trimmed to register, map, keepalive, deltas and ephemeral expiry: no
// database, CLI, OIDC, policy language, DNS, routes, SSH or Taildrop.
package coordinator

import (
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/worker"
	"golang.org/x/net/http2"
	"tailscale.com/control/controlhttp/controlhttpserver"
	"tailscale.com/envknob"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/types/key"
	"tailscale.com/util/zstdframe"
)

// Prefix holds every node's address, prefix::N. It is one random ULA /48, so
// it meets no LAN and not Tailscale's 100.64/10 or fd7a:115c:a1e0::/48.
var Prefix = netip.MustParsePrefix("fd7c:9a2e:4b10::/48")

const (
	// The tunnel closes a response idle for about 100s. Headscale's 50s plus
	// up to 9s of jitter stays under 60s.
	keepAlive = 50 * time.Second
	jitter    = 9 * time.Second

	// Headscale's limit: no register or map request comes near it, and the
	// Noise handshake takes any machine key, so everything behind it is open.
	bodyLimit = 1 << 20

	// The protocol needs a user and a relay region; a project has one of each.
	user   = tailcfg.UserID(1)
	region = 900

	// A self-hosted harness has one project, and nothing names it yet.
	// Workers read it from the map's Domain for their long names,
	// <worker>--<project>.mainplane.net.
	project = "home"

	// An ephemeral node goes this long after its last map poll ends. It is
	// over twice the longest gap a live worker has between polls, a harness
	// moving to a new quick URL (80s measured), and short enough that a fleet
	// that finished is gone within minutes.
	idle = 3 * time.Minute

	// With no frame from a node for this long, the coordinator pings it, and
	// a node that does not answer in 15s is gone. Only a closed connection
	// ends a poll otherwise, and the tunnel keeps its side of a dead
	// worker's open.
	pingIdle = time.Minute
)

type node struct {
	ID      tailcfg.NodeID    `json:"id"`
	Name    string            `json:"name"`
	Machine key.MachinePublic `json:"machine"`
	Key     key.NodePublic    `json:"key"`

	// An ephemeral node goes idle after its last poll. A removed one stays,
	// so its keys cannot join again, but no node sees it and it sees none.
	Ephemeral bool `json:"ephemeral,omitempty"`
	Removed   bool `json:"removed,omitempty"`

	// From map requests. They are kept, as headscale does, because a node
	// that reconnects after a restart sends only what changed, and peers
	// need its disco key and home relay to reach it until then.
	Disco     key.DiscoPublic   `json:"disco"`
	Endpoints []netip.AddrPort  `json:"endpoints"`
	Hostinfo  *tailcfg.Hostinfo `json:"hostinfo"`

	capVer  tailcfg.CapabilityVersion
	streams int       // open map polls
	quiet   time.Time // when the last poll ended
	ver     uint64    // bumped on each change a peer must see
}

type state struct {
	Next  tailcfg.NodeID `json:"next"`
	Nodes []*node        `json:"nodes"`
}

// Coordinator holds the nodes of one project.
type Coordinator struct {
	key  key.MachinePrivate
	file string
	join func(secret, addr string) (ephemeral bool, err error)
	self string // the harness's own node registers with it; never on disk

	mu    sync.Mutex
	st    state
	host  string // the front door's, where the relay is
	ver   uint64
	wakes map[chan struct{}]bool // one per map poll
}

// New loads the Noise key and the nodes from dir, made there the first time.
// join checks a join secret sent from addr and says whether it makes
// ephemeral nodes. Each ephemeral node loaded has idle from now to poll
// again.
func New(dir string, join func(secret, addr string) (bool, error)) (*Coordinator, error) {
	k, err := noiseKey(filepath.Join(dir, "noise.key"))
	if err != nil {
		return nil, err
	}
	c := &Coordinator{key: k, file: filepath.Join(dir, "nodes.json"), join: join, self: crand.Text(), st: state{Next: 1}, wakes: map[chan struct{}]bool{}}
	b, err := os.ReadFile(c.file)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &c.st); err != nil {
		return nil, err
	}
	for _, n := range c.st.Nodes {
		c.expire(n)
	}
	return c, nil
}

// noiseKey is the coordinator's machine key kept in file. Whoever has it can
// stand in for the coordinator, so only its owner may read it.
func noiseKey(file string) (key.MachinePrivate, error) {
	k := key.NewMachine()
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		b, _ = k.MarshalText()
		return k, os.WriteFile(file, b, 0o600)
	}
	if err != nil {
		return k, err
	}
	return k, k.UnmarshalText(b)
}

// Public is the Noise key workers fetch at /key. The harness key vouches for
// it in /id, because TLS ends at the tunnel.
func (c *Coordinator) Public() key.MachinePublic { return c.key.Public() }

// Relay puts the relay at the host of the harness's URL u in every node's map.
func (c *Coordinator) Relay(u string) {
	pu, err := url.Parse(u)
	if err != nil {
		log.Printf("coordinator: relay at %q: %v", u, err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.host = pu.Hostname()
	c.wake()
}

// Listen puts the harness on its own mesh as a node in userspace, no TUN and
// no root, with its keys in dir so its address holds across restarts. It
// registers at control, this coordinator on loopback, so it needs neither the
// tunnel nor a proof, with the secret only this process holds, and is named
// worker.Harness. The listener takes port on the node's address, which only
// nodes the coordinator gave keys to can reach. It all closes when ctx ends.
func (c *Coordinator) Listen(ctx context.Context, dir, control string, port int) (net.Listener, error) {
	// The node reaches the relay through the tunnel as every node does, and
	// Cloudflare answers 400 to the native upgrade.
	envknob.Setenv("TS_DEBUG_DERP_WS_CLIENT", "1")
	// A worker's keepalive holds its connection; these end one whose worker
	// vanished in about a minute, not in netstack's two hours.
	envknob.Setenv("TS_NETSTACK_KEEPALIVE_IDLE", "15s")
	envknob.Setenv("TS_NETSTACK_KEEPALIVE_INTERVAL", "5s")
	logf := func(format string, a ...any) {
		if !strings.Contains(format, "[v") { // tailscale's verbose levels
			log.Printf("mesh: "+format, a...)
		}
	}
	s := &tsnet.Server{Dir: dir, Hostname: worker.Harness, ControlURL: control, AuthKey: c.self, Logf: logf, UserLogf: logf}
	st, err := s.Up(ctx)
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	if err != nil {
		return nil, err
	}
	l, err := s.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	log.Printf("coordinator: the harness is %v on the mesh, port %d", st.TailscaleIPs, port)
	return l, nil
}

// Known is whether k is a registered node's. The relay admits no other.
func (c *Coordinator) Known(k key.NodePublic) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.byKey(k)
	return n != nil && !n.Removed
}

// Node is the name of the registered node at address a.
func (c *Coordinator) Node(a netip.Addr) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.st.Nodes, func(n *node) bool { return address(n.ID) == a && !n.Removed })
	if i < 0 {
		return "", false
	}
	return c.st.Nodes[i].Name, true
}

// Remove takes the worker named name off the mesh for good. Its poll ends
// and peers drop it at once. Its next poll finds it no longer authorized,
// which its worker takes as the word to leave. Its keys stay refused; the
// same machine joins again only with new keys, as a new install makes.
func (c *Coordinator) Remove(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.st.Nodes, func(n *node) bool { return n.Name == name && !n.Removed })
	if i < 0 || name == worker.Harness {
		return fmt.Errorf("no worker %q on the mesh", name)
	}
	n := c.st.Nodes[i]
	n.Removed = true
	c.changed(n)
	log.Printf("coordinator: %s removed", n.Name)
	return c.save()
}

// expire deletes ephemeral node n once it has been idle with no poll. The
// caller holds mu, or is alone.
func (c *Coordinator) expire(n *node) {
	if !n.Ephemeral {
		return
	}
	n.quiet = time.Now()
	time.AfterFunc(idle, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if n.streams > 0 || time.Since(n.quiet) < idle || !slices.Contains(c.st.Nodes, n) {
			return
		}
		c.st.Nodes = slices.DeleteFunc(c.st.Nodes, func(p *node) bool { return p == n })
		c.wake()
		if err := c.save(); err != nil {
			log.Printf("coordinator: %v", err)
		}
		log.Printf("coordinator: %s expired, no map poll for %s", n.Name, idle)
	})
}

// Handle serves the Noise key at /key and the protocol at /ts2021 on mux.
func (c *Coordinator) Handle(mux *http.ServeMux) {
	mux.HandleFunc("GET /key", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tailcfg.OverTLSPublicKeyResponse{PublicKey: c.key.Public()})
	})
	mux.HandleFunc("/ts2021", c.upgrade)
}

// upgrade is headscale's NoiseUpgradeHandler: HTTP/2 over the Noise
// connection, with register and map.
func (c *Coordinator) upgrade(w http.ResponseWriter, r *http.Request) {
	addr := auth.Addr(r)
	conn, err := controlhttpserver.AcceptHTTP(r.Context(), w, r, c.key, nil)
	if err != nil {
		log.Printf("coordinator: %s: %v", addr, err)
		return
	}
	defer func() { _ = conn.Close() }()
	machine := conn.Peer()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /machine/register", func(w http.ResponseWriter, r *http.Request) {
		var req tailcfg.RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c.register(machine, addr, req))
	})
	mux.HandleFunc("POST /machine/map", func(w http.ResponseWriter, r *http.Request) {
		c.serveMap(w, r, machine)
	})
	h2 := http2.Server{ReadIdleTimeout: pingIdle}
	h2.ServeConn(conn, &http2.ServeConnOpts{Context: r.Context(), BaseConfig: &http.Server{Handler: http.MaxBytesHandler(mux, bodyLimit)}})
}

// register is headscale's handleRegister for pre-auth keys, where the key is
// the join secret. A node key already registered to the machine needs none.
func (c *Coordinator) register(machine key.MachinePublic, addr string, req tailcfg.RegisterRequest) tailcfg.RegisterResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.byKey(req.NodeKey)
	if n != nil && n.Machine != machine {
		return tailcfg.RegisterResponse{Error: "node key registered to another machine"}
	}
	// A past expiry is a logout, and a node that leaves is gone.
	if !req.Expiry.IsZero() && req.Expiry.Before(time.Now()) {
		if n != nil {
			c.st.Nodes = slices.DeleteFunc(c.st.Nodes, func(p *node) bool { return p == n })
			c.wake()
			if err := c.save(); err != nil {
				return tailcfg.RegisterResponse{Error: err.Error()}
			}
			log.Printf("coordinator: %s left", n.Name)
		}
		return tailcfg.RegisterResponse{NodeKeyExpired: true}
	}
	if n == nil {
		var secret string
		if req.Auth != nil {
			secret = req.Auth.AuthKey
		}
		self, ephemeral := subtle.ConstantTimeCompare([]byte(secret), []byte(c.self)) == 1, false
		if !self {
			var err error
			if ephemeral, err = c.join(secret, addr); err != nil {
				return tailcfg.RegisterResponse{Error: err.Error()}
			}
		}
		// A machine that registers a new node key keeps its address and name.
		if n = c.byMachine(machine); n == nil {
			name := c.name(req.Hostinfo)
			if self {
				// A harness whose node lost its keys replaces its old node.
				name = worker.Harness
				c.st.Nodes = slices.DeleteFunc(c.st.Nodes, func(p *node) bool { return p.Name == name })
			}
			n = &node{ID: c.st.Next, Name: name, Machine: machine, Ephemeral: ephemeral}
			c.st.Next++
			c.st.Nodes = append(c.st.Nodes, n)
			c.expire(n)
		}
		n.Key, n.Hostinfo = req.NodeKey, req.Hostinfo
		c.changed(n)
		if err := c.save(); err != nil {
			return tailcfg.RegisterResponse{Error: err.Error()}
		}
		log.Printf("coordinator: %s joined as %s from %s, ephemeral %v", n.Name, address(n.ID), addr, n.Ephemeral)
	}
	return tailcfg.RegisterResponse{
		MachineAuthorized: true,
		User:              tailcfg.User{ID: user, DisplayName: "mainplane"},
		Login:             tailcfg.Login{ID: tailcfg.LoginID(user), LoginName: "mainplane", DisplayName: "mainplane"},
	}
}

// serveMap is headscale's PollNetMapHandler. A poll gets the whole map, then
// what changes, then a keepalive when nothing has for a while. A request
// that does not poll only updates the node.
func (c *Coordinator) serveMap(w http.ResponseWriter, r *http.Request, machine key.MachinePublic) {
	var req tailcfg.MapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	n := c.byKey(req.NodeKey)
	if n == nil || n.Machine != machine {
		c.mu.Unlock()
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	c.update(n, req)
	if !req.Stream {
		c.mu.Unlock()
		return
	}
	wake := make(chan struct{}, 1)
	c.wakes[wake] = true
	n.streams++
	c.changed(n)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.wakes, wake)
		if n.streams--; n.streams == 0 {
			c.expire(n)
		}
		c.changed(n)
		c.mu.Unlock()
	}()

	sent, host := map[tailcfg.NodeID]uint64{}, ""
	ka := keepAlive + rand.N(jitter)
	tick := time.NewTicker(ka)
	defer tick.Stop()
	for full := true; ; full = false {
		c.mu.Lock()
		resp, ok := c.delta(n, sent, &host, full)
		c.mu.Unlock()
		if !ok {
			return
		}
		if resp != nil {
			if err := write(w, req.Compress, resp); err != nil {
				return
			}
			tick.Reset(ka)
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-tick.C:
			if err := write(w, req.Compress, &tailcfg.MapResponse{KeepAlive: true}); err != nil {
				return
			}
		}
	}
}

// update takes what a map request says about n. Peers see a new disco key,
// endpoints or home relay; the rest they do not need.
func (c *Coordinator) update(n *node, req tailcfg.MapRequest) {
	moved := false
	if !req.DiscoKey.IsZero() && req.DiscoKey != n.Disco {
		n.Disco, moved = req.DiscoKey, true
	}
	if req.Endpoints != nil && !slices.Equal(req.Endpoints, n.Endpoints) {
		n.Endpoints, moved = req.Endpoints, true
	}
	if hi := req.Hostinfo; hi != nil {
		// A client sends NetInfo only when it changed.
		if hi = hi.Clone(); hi.NetInfo == nil && n.Hostinfo != nil {
			hi.NetInfo = n.Hostinfo.NetInfo
		}
		moved = moved || home(hi) != home(n.Hostinfo)
		n.Hostinfo = hi
	}
	n.capVer = req.Version
	if moved {
		c.changed(n)
		if err := c.save(); err != nil {
			log.Printf("coordinator: %v", err)
		}
	}
}

// delta is what n has not seen: every peer it sees whose version differs
// from sent, peers gone from its sight since, and the relay map when the host
// moved from host. It is nil when there is nothing, and not ok once n itself
// is gone, or removed since the full map, which told it so.
func (c *Coordinator) delta(n *node, sent map[tailcfg.NodeID]uint64, host *string, full bool) (*tailcfg.MapResponse, bool) {
	if !slices.Contains(c.st.Nodes, n) || n.Removed && !full {
		return nil, false
	}
	r := &tailcfg.MapResponse{}
	if full {
		r.Node = tail(n)
		r.Domain = project
		r.PacketFilter = tailcfg.FilterAllowAll
		r.UserProfiles = []tailcfg.UserProfile{{ID: user, LoginName: "mainplane", DisplayName: "mainplane"}}
	}
	if c.host != *host {
		*host = c.host
		r.DERPMap = c.relayMap()
	}
	live := map[tailcfg.NodeID]bool{}
	for _, p := range c.st.Nodes {
		if p == n || !sees(n, p) {
			continue
		}
		live[p.ID] = true
		if v, ok := sent[p.ID]; ok && v == p.ver {
			continue
		}
		sent[p.ID] = p.ver
		if full {
			r.Peers = append(r.Peers, tail(p))
		} else {
			r.PeersChanged = append(r.PeersChanged, tail(p))
		}
	}
	for id := range sent {
		if !live[id] {
			r.PeersRemoved = append(r.PeersRemoved, id)
			delete(sent, id)
		}
	}
	if !full && r.DERPMap == nil && r.PeersChanged == nil && r.PeersRemoved == nil {
		return nil, true
	}
	return r, true
}

// sees is whether a and b are in each other's map, which is the whole access
// rule: WireGuard takes packets only from peers in the map, at both ends. The
// harness sees every node and every node sees it, as will the file server
// once there is one. Persistent workers, a user's own machines, see each
// other. An ephemeral worker, one of a fleet, sees only those. A removed node
// sees none and is seen by none.
func sees(a, b *node) bool {
	switch {
	case a.Removed || b.Removed:
		return false
	case a.Name == worker.Harness || b.Name == worker.Harness:
		return true
	}
	return !a.Ephemeral && !b.Ephemeral
}

// tail is headscale's TailNode, trimmed: one address, no routes, no caps. A
// removed node is no longer authorized.
func tail(n *node) *tailcfg.Node {
	a := netip.PrefixFrom(address(n.ID), 128)
	online := n.streams > 0
	return &tailcfg.Node{
		ID:                n.ID,
		StableID:          tailcfg.StableNodeID(strconv.FormatInt(int64(n.ID), 10)),
		Name:              n.Name,
		User:              user,
		Key:               n.Key,
		Machine:           n.Machine,
		DiscoKey:          n.Disco,
		Addresses:         []netip.Prefix{a},
		AllowedIPs:        []netip.Prefix{a},
		Endpoints:         n.Endpoints,
		HomeDERP:          home(n.Hostinfo),
		Hostinfo:          n.Hostinfo.View(),
		Cap:               n.capVer,
		MachineAuthorized: !n.Removed,
		Online:            &online,
	}
}

// relayMap has the harness's relay first, because a WebSocket client dials
// the first node of a region, then two public STUN servers on different IPs,
// because telling a hard NAT needs two.
func (c *Coordinator) relayMap() *tailcfg.DERPMap {
	return &tailcfg.DERPMap{Regions: map[int]*tailcfg.DERPRegion{region: {
		RegionID:   region,
		RegionCode: "mp",
		RegionName: "harness",
		Nodes: []*tailcfg.DERPNode{
			{Name: "harness", RegionID: region, HostName: c.host, DERPPort: 443, STUNPort: -1},
			{Name: "stun-cloudflare", RegionID: region, HostName: "stun.cloudflare.com", STUNPort: 3478, STUNOnly: true},
			{Name: "stun-google", RegionID: region, HostName: "stun.l.google.com", STUNPort: 19302, STUNOnly: true},
		},
	}}}
}

// write is headscale's writeMapResponse: JSON, zstd framed when the client
// asks, behind its little-endian length.
func write(w http.ResponseWriter, compress string, r *tailcfg.MapResponse) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if compress == "zstd" {
		b = zstdframe.AppendEncode(nil, b, zstdframe.FastestCompression)
	}
	if _, err := w.Write(append(binary.LittleEndian.AppendUint32(nil, uint32(len(b))), b...)); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

// name is the hostname's first label in lower case letters, digits and
// single dashes, made unique with -2, -3. No name has "--", the separator of
// long names, none is "xn", because a label starting "xn--" is punycode, and
// none is the harness's.
func (c *Coordinator) name(hi *tailcfg.Hostinfo) string {
	var h string
	if hi != nil {
		h, _, _ = strings.Cut(strings.ToLower(hi.Hostname), ".")
	}
	var b []byte
	for _, r := range h {
		switch {
		case 'a' <= r && r <= 'z' || '0' <= r && r <= '9':
			b = append(b, byte(r))
		case len(b) > 0 && b[len(b)-1] != '-':
			b = append(b, '-')
		}
	}
	base := strings.TrimSuffix(string(b), "-")
	if base == "" {
		base = "worker"
	}
	name := base
	for i := 2; name == "xn" || name == worker.Harness || slices.ContainsFunc(c.st.Nodes, func(p *node) bool { return p.Name == name && !p.Removed }); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func address(id tailcfg.NodeID) netip.Addr {
	a := Prefix.Addr().As16()
	binary.BigEndian.PutUint64(a[8:], uint64(id))
	return netip.AddrFrom16(a)
}

func home(hi *tailcfg.Hostinfo) int {
	if hi == nil || hi.NetInfo == nil {
		return 0
	}
	return hi.NetInfo.PreferredDERP
}

// changed gives n a new version, so every poll sends it again.
func (c *Coordinator) changed(n *node) {
	c.ver++
	n.ver = c.ver
	c.wake()
}

func (c *Coordinator) wake() {
	for ch := range c.wakes {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (c *Coordinator) byKey(k key.NodePublic) *node {
	i := slices.IndexFunc(c.st.Nodes, func(n *node) bool { return n.Key == k })
	if i < 0 {
		return nil
	}
	return c.st.Nodes[i]
}

func (c *Coordinator) byMachine(k key.MachinePublic) *node {
	i := slices.IndexFunc(c.st.Nodes, func(n *node) bool { return n.Machine == k })
	if i < 0 {
		return nil
	}
	return c.st.Nodes[i]
}

func (c *Coordinator) save() error {
	b, err := json.MarshalIndent(c.st, "", "  ")
	if err != nil {
		return err
	}
	// A node's endpoints change often, so a crash mid-write must leave the
	// last registry whole.
	if err := os.WriteFile(c.file+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(c.file+".tmp", c.file)
}
