package vm

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// MinHostPort is the lowest port Lima can expose on the Mac without root.
	MinHostPort = 1024
	// MaxPort is the highest TCP port.
	MaxPort = 65535
)

// Forward maps 127.0.0.1:Host on the Mac to port Guest inside a microVM.
type Forward struct {
	Host  int    `json:"host"`
	Guest int    `json:"guest"`
	State string `json:"state,omitempty"`
}

// ParseForward accepts "PORT" (same port on both sides) or "HOST:GUEST".
func ParseForward(spec string) (Forward, error) {
	spec = strings.TrimSpace(spec)
	hostText, guestText := spec, spec
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		hostText, guestText = spec[:i], spec[i+1:]
	}
	host, err := parsePort(hostText, MinHostPort, "host")
	if err != nil {
		return Forward{}, fmt.Errorf("forward %q: %w", spec, err)
	}
	guest, err := parsePort(guestText, 1, "guest")
	if err != nil {
		return Forward{}, fmt.Errorf("forward %q: %w", spec, err)
	}
	return Forward{Host: host, Guest: guest}, nil
}

// Spec renders the canonical HOST:GUEST form understood by the agent.
func (f Forward) Spec() string {
	return strconv.Itoa(f.Host) + ":" + strconv.Itoa(f.Guest)
}

// Label renders the forward for humans, e.g. "8080→80".
func (f Forward) Label() string {
	return strconv.Itoa(f.Host) + "→" + strconv.Itoa(f.Guest)
}

func parsePort(text string, min int, label string) (int, error) {
	port, err := strconv.Atoi(text)
	if err != nil || port < min || port > MaxPort {
		return 0, fmt.Errorf("%s port must be a number between %d and %d, got %q", label, min, MaxPort, text)
	}
	return port, nil
}
