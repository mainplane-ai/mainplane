package mesh

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
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
	a, err := Addr()
	if err != nil {
		return netip.Prefix{}, err
	}
	return project(netip.PrefixFrom(a, 128)), nil
}

// Addr is this machine's address on the mesh, for a server that listens on
// the mesh only.
func Addr() (netip.Addr, error) {
	ifc, err := net.InterfaceByName(tunName)
	if err != nil {
		return netip.Addr{}, err
	}
	as, err := ifc.Addrs()
	if err != nil {
		return netip.Addr{}, err
	}
	for _, a := range as {
		if p, err := netip.ParsePrefix(a.String()); err == nil && !p.Addr().IsLinkLocalUnicast() {
			return p.Addr(), nil
		}
	}
	return netip.Addr{}, errors.New("no address on the mesh yet")
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

// watchRules calls f each time an IPv6 rule of this machine goes, until
// the func it returns stops it. Each time systemd-networkd configures a
// link, as at every start, it removes every rule it did not make, ours too.
// Without the rule a connection to the harness stalls until TCP gives up,
// minutes later. A rule of tailscale's own router comes back the same way,
// but its monitor hears IPv4 rules only.
func watchRules(f func()) (func() error, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("watching rules: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1 << (unix.RTNLGRP_IPV6_RULE - 1)}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("watching rules: %w", err)
	}
	// On the runtime's poller, so that Close ends a Read that waits.
	s := os.NewFile(uintptr(fd), "netlink")
	go func() {
		b := make([]byte, 1<<16)
		for {
			n, err := s.Read(b)
			switch {
			case errors.Is(err, os.ErrClosed):
				return
			case errors.Is(err, unix.ENOBUFS): // messages were lost, a deletion maybe
				f()
			case err != nil:
				log.Printf("mesh: watching rules: %v", err)
				return
			default:
				ms, err := syscall.ParseNetlinkMessage(b[:n])
				if err != nil {
					log.Printf("mesh: watching rules: %v", err) // a deletion maybe
				}
				if err != nil || slices.ContainsFunc(ms, func(m syscall.NetlinkMessage) bool { return m.Header.Type == unix.RTM_DELRULE }) {
					f()
				}
			}
		}
	}()
	return s.Close, nil
}

// keep puts back a's route and rule when something took them away.
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
