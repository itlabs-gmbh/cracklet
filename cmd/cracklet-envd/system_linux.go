//go:build linux

package main

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// linuxSystem applies identities with netlink and syscalls, without spawning processes.
type linuxSystem struct{}

func (linuxSystem) ReplaceAddress(iface, cidr string) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return err
	}
	existing, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for i := range existing {
		if err := netlink.AddrDel(link, &existing[i]); err != nil {
			return fmt.Errorf("remove %s: %w", existing[i].IPNet, err)
		}
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add %s: %w", cidr, err)
	}
	return netlink.LinkSetUp(link)
}

func (linuxSystem) ReplaceDefaultRoute(iface, gateway string) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return err
	}
	gw := net.ParseIP(gateway)
	if gw == nil {
		return fmt.Errorf("invalid gateway %q", gateway)
	}
	return netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: gw})
}

func (linuxSystem) SetHostname(name string) error {
	return unix.Sethostname([]byte(name))
}

func (linuxSystem) SetTime(sec int64) error {
	return unix.Settimeofday(&unix.Timeval{Sec: sec})
}
