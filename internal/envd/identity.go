// Package envd implements the guest-side identity service. After a microVM is
// restored from the golden snapshot it still carries the snapshot's IP address,
// hostname and clock; the host agent sends the real identity over vsock and
// envd applies it.
package envd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
)

// Port is the vsock port envd listens on inside the guest.
const Port = 52

// Identity is the message sent by the host agent after a restore.
type Identity struct {
	IP       string `json:"ip"`
	Prefix   int    `json:"prefix"`
	Gateway  string `json:"gateway"`
	Hostname string `json:"hostname"`
	Iface    string `json:"iface"`
	// UnixTime resets the guest clock, which is frozen at snapshot time.
	UnixTime int64 `json:"unix_time"`
	// Seed is base64 random data mixed into the guest's entropy pool so
	// clones do not share their random state.
	Seed string `json:"seed,omitempty"`
}

// CIDR renders the address with its prefix length.
func (id Identity) CIDR() string {
	return id.IP + "/" + strconv.Itoa(id.Prefix)
}

// Parse decodes and validates an identity message.
func Parse(data []byte) (Identity, error) {
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return Identity{}, fmt.Errorf("decode identity: %w", err)
	}
	if net.ParseIP(id.IP) == nil || net.ParseIP(id.Gateway) == nil {
		return Identity{}, fmt.Errorf("identity needs a valid ip and gateway, got %q/%q", id.IP, id.Gateway)
	}
	if id.Prefix < 1 || id.Prefix > 32 {
		return Identity{}, fmt.Errorf("invalid prefix %d", id.Prefix)
	}
	if id.Iface == "" {
		id.Iface = "eth0"
	}
	if !validHostname(id.Hostname) {
		return Identity{}, fmt.Errorf("invalid hostname %q", id.Hostname)
	}
	if id.UnixTime <= 0 {
		return Identity{}, fmt.Errorf("invalid unix_time %d", id.UnixTime)
	}
	if id.Seed != "" {
		if _, err := base64.StdEncoding.DecodeString(id.Seed); err != nil {
			return Identity{}, fmt.Errorf("invalid seed: %w", err)
		}
	}
	return id, nil
}

func validHostname(h string) bool {
	if h == "" || len(h) > 63 {
		return false
	}
	for i, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// System is the set of kernel operations an identity needs. The Linux
// implementation talks netlink and syscalls directly: right after a snapshot
// restore every page the guest touches is a nested page fault, so spawning
// `ip` seven times costs well over a second while in-process calls cost
// milliseconds.
type System interface {
	// ReplaceAddress removes all IPv4 addresses from iface and adds cidr.
	ReplaceAddress(iface, cidr string) error
	// ReplaceDefaultRoute sets the IPv4 default route via gateway on iface.
	ReplaceDefaultRoute(iface, gateway string) error
	SetHostname(name string) error
	SetTime(unix int64) error
}

// Apply reconfigures the guest. hostnameFile and randomDev are parameters so
// tests can point them at temp files.
func Apply(id Identity, sys System, hostnameFile, randomDev string) error {
	if err := sys.ReplaceAddress(id.Iface, id.CIDR()); err != nil {
		return fmt.Errorf("address: %w", err)
	}
	if err := sys.ReplaceDefaultRoute(id.Iface, id.Gateway); err != nil {
		return fmt.Errorf("route: %w", err)
	}
	if err := sys.SetHostname(id.Hostname); err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	if err := sys.SetTime(id.UnixTime); err != nil {
		return fmt.Errorf("clock: %w", err)
	}
	if err := os.WriteFile(hostnameFile, []byte(id.Hostname+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", hostnameFile, err)
	}
	if id.Seed != "" {
		seed, _ := base64.StdEncoding.DecodeString(id.Seed)
		if err := os.WriteFile(randomDev, seed, 0o600); err != nil {
			return fmt.Errorf("seed %s: %w", randomDev, err)
		}
	}
	return nil
}
