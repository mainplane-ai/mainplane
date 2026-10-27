package mesh

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

const (
	tunName = "mainplane0"

	// Tailscale's rules start at 5210 and send everything else to its table
	// 52, where an exit node puts a default route. The /48 route sits in a
	// table of ours, behind a rule that comes first, so no exit node takes
	// it. The table's number names the rule when it is removed.
	priority = "5200"
	table    = "5200"
)

// Network is the project's /48, from this machine's address on the mesh,
// for a server that lets in only the mesh.
func Network() (netip.Prefix, error) {
	ifc, err := net.InterfaceByName(tunName)
	if err != nil {
		return netip.Prefix{}, err
	}
	as, err := ifc.Addrs()
	if err != nil {
		return netip.Prefix{}, err
	}
	for _, a := range as {
		if p, err := netip.ParsePrefix(a.String()); err == nil && !p.Addr().IsLinkLocalUnicast() {
			return project(p), nil
		}
	}
	return netip.Prefix{}, errors.New("no address on the mesh yet")
}

// alone has nothing to check: a second worker's TUN of the same name fails
// as busy.
func alone() error { return nil }

func (r *osRouter) Up() error { return run("ip", "link", "set", "dev", r.tun, "up") }

// add gives tun address a, and the rule and route that send a's /48 to it.
// A rule a killed worker left goes first; its route and address went with
// its TUN.
func add(tun string, a netip.Prefix) error {
	dropRule()
	p := project(a).String()
	for _, args := range [][]string{
		{"-6", "addr", "replace", a.String(), "dev", tun, "nodad"},
		{"-6", "route", "replace", p, "dev", tun, "table", table},
		{"-6", "rule", "add", "to", p, "table", table, "priority", priority},
	} {
		if err := run("ip", args...); err != nil {
			return err
		}
	}
	return nil
}

// keep puts back a's route and rule when something took them away, as
// systemd-networkd does with every rule it did not make when it starts.
func keep(tun string, a netip.Prefix) error {
	p := project(a).String()
	if err := run("ip", "-6", "route", "replace", p, "dev", tun, "table", table); err != nil {
		return err
	}
	args := []string{"-6", "rule", "list", "to", p, "table", table, "priority", priority}
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, out)
	}
	if len(out) > 0 {
		return nil
	}
	if err := run("ip", "-6", "rule", "add", "to", p, "table", table, "priority", priority); err != nil {
		return err
	}
	log.Printf("mesh: the rule to %s was gone: added again", p)
	return nil
}

func del(tun string, a netip.Prefix) error {
	dropRule()
	if err := run("ip", "-6", "route", "flush", "table", table); err != nil {
		return err
	}
	return run("ip", "-6", "addr", "del", a.String(), "dev", tun)
}

// dropRule removes every rule to our table, which outlives a worker that
// was killed.
func dropRule() {
	for run("ip", "-6", "rule", "del", "table", table) == nil {
	}
}
