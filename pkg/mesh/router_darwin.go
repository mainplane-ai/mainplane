package mesh

import "net/netip"

// macOS names every TUN itself: utun and the next free number.
const tunName = "utun"

func (r *osRouter) Up() error { return run("ifconfig", r.tun, "up") }

// add gives tun address a and routes a's /48 to it. Both go with the TUN
// when a worker is killed.
func add(tun string, a netip.Prefix) error {
	if err := run("ifconfig", tun, "inet6", a.String(), a.Addr().String()); err != nil {
		return err
	}
	return run("route", "-q", "-n", "add", "-inet6", project(a), "-iface", tun)
}

// dropRule has nothing to drop: the route goes with the TUN.
func dropRule() {}

func del(tun string, a netip.Prefix) error {
	if err := run("route", "-q", "-n", "delete", "-inet6", project(a), "-iface", tun); err != nil {
		return err
	}
	return run("ifconfig", tun, "inet6", a.String(), "-alias")
}
