package container

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/shquote"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// SSHProxyCommand implements backend.Backend: ssh runs cracklet itself, which
// resolves the machine's current vmnet address on every connection. ssh
// expands % tokens in the line, so a % in the path is escaped.
func (b *Backend) SSHProxyCommand() string {
	return shquote.Quote(strings.ReplaceAll(b.exe, "%", "%%")) + " _dial %h %p"
}

// Dial implements backend.Backend. The address comes from `machine ls`; on
// vmnet every guest shares one /24 and nothing stops a guest from taking
// another guest's address, while the generated ssh_config does not verify
// host keys. A hostile guest could therefore pose as its neighbour towards the
// Mac. The Lima backend is not exposed to this (one /30 and a firewall per
// guest); for this backend, pinning each VM's host key at creation is part of
// Phase 4 (docs/design/container-backend.md, "Network isolation").
func (b *Backend) Dial(ctx context.Context, name string, port int) (backend.Conn, error) {
	m, err := b.machine(ctx, name)
	if err != nil {
		return nil, err
	}
	if m.state() != vm.StateRunning || m.addr() == "" {
		return nil, fmt.Errorf("VM '%s' is %s (cracklet start %s)", name, m.state(), name)
	}
	return b.dial(ctx, net.JoinHostPort(m.addr(), strconv.Itoa(port)))
}

// tcpDial is the production Dialer. The timeout matters for the ProxyCommand,
// whose context only ends with a signal.
func tcpDial(ctx context.Context, addr string) (backend.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 10 * dialAttempt}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected connection type %T", conn)
	}
	return tcp, nil
}
