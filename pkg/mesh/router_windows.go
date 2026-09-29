package mesh

import (
	"net/netip"

	"github.com/tailscale/wireguard-go/tun"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"tailscale.com/net/tstun"
)

const (
	// The wintun adapter's name. Tailscale's is Tailscale.
	tunName = "Mainplane"
	// Windows Defender Firewall blocks what peers open to this machine: the
	// rule of this name lets in the project's /48 to this node's address.
	// Windows takes a packet only on the interface that has its address.
	rule = "Mainplane"
	// Windows' own metrics run 5 to 75, and a new adapter gets 5, the
	// lowest: multicast and broadcast would leave by the mesh, and so would
	// 169.254/16, since an adapter with no IPv4 address takes a link-local
	// one and its route, and Windows lets no one turn that off at run time.
	// Only the /48 has to leave by the mesh, and no other adapter routes it.
	metric = 1000
)

// tstun asks wintun for Tailscale's adapter GUID, which would take the
// user's Tailscale adapter. One of our own that never changes keeps Windows
// to one network profile for the mesh.
var guid = windows.GUID{Data1: 0xc6c3abb4, Data2: 0xad0a, Data3: 0x4f1b, Data4: [8]byte{0x9b, 0xcd, 0x90, 0x3d, 0x3a, 0x19, 0xac, 0x58}}

func init() {
	tun.WintunTunnelType = tunName
	tun.WintunStaticRequestedGUID = &guid
}

// Up has nothing to do: wintun's adapter is up once made.
func (r *osRouter) Up() error { return nil }

// add gives the adapter address a, routes a's /48 to it, and lets peers in.
// The address and route go with the adapter when a worker is killed; the
// firewall rule stays until the next start or uninstall.
func add(_ string, a netip.Prefix) error {
	// The rule first, so a netsh that fails leaves no address behind.
	dropRule()
	if err := run("netsh", "advfirewall", "firewall", "add", "rule", "name="+rule, "dir=in", "action=allow", "protocol=any",
		"localip="+a.Addr().String(), "remoteip="+project(a).String()); err != nil {
		return err
	}
	luid, err := winipcfg.LUIDFromGUID(&guid)
	if err != nil {
		return err
	}
	v4, err := luid.IPInterface(windows.AF_INET)
	if err != nil {
		return err
	}
	v4.UseAutomaticMetric, v4.Metric = false, metric
	if err := v4.Set(); err != nil {
		return err
	}
	// No duplicate address detection, which holds the address back a
	// second, and no router discovery: the only router is this node.
	v6, err := luid.IPInterface(windows.AF_INET6)
	if err != nil {
		return err
	}
	v6.DadTransmits, v6.RouterDiscoveryBehavior, v6.NLMTU = 0, winipcfg.RouterDiscoveryDisabled, uint32(tstun.DefaultTUNMTU())
	v6.UseAutomaticMetric, v6.Metric = false, metric
	if err := v6.Set(); err != nil {
		return err
	}
	// Nor is this node's name in DNS at its mesh address, as Tailscale keeps
	// its own out.
	for _, stack := range []string{"Tcpip", "Tcpip6"} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+stack+`\Parameters\Interfaces\`+guid.String(), registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetDWordValue("RegistrationEnabled", 0)
		_ = k.Close()
		if err != nil {
			return err
		}
	}
	if err := luid.AddIPAddress(a); err != nil {
		return err
	}
	return luid.AddRoute(project(a), netip.IPv6Unspecified(), 0)
}

// dropRule removes the firewall rule, which outlives a worker that was killed.
func dropRule() { _ = run("netsh", "advfirewall", "firewall", "delete", "rule", "name="+rule) }

func del(_ string, a netip.Prefix) error {
	dropRule()
	luid, err := winipcfg.LUIDFromGUID(&guid)
	if err != nil {
		return err
	}
	if err := luid.DeleteRoute(project(a), netip.IPv6Unspecified()); err != nil {
		return err
	}
	return luid.DeleteIPAddress(a)
}
