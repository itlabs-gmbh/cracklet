package container

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/shquote"
	"github.com/itlabs-gmbh/skiff/internal/vm"
)

// SSHProxyCommand implements backend.Backend: ssh runs skiff itself, which
// resolves the machine's current vmnet address on every connection. ssh
// expands % tokens in the line, so a % in the path is escaped.
func (b *Backend) SSHProxyCommand() string {
	return shquote.Quote(strings.ReplaceAll(b.exe, "%", "%%")) + " _dial %h %p"
}

// KnownHostsFile implements backend.Backend: the host keys read from every
// VM at creation (see pinHostKeys), so ssh refuses a neighbour on the shared
// vmnet that answers under another VM's address.
func (b *Backend) KnownHostsFile() string { return b.paths.KnownHostsPath() }

// Dial implements backend.Backend. The address comes from `machine ls`; on
// vmnet every guest shares one /24 and nothing stops a guest from taking
// another guest's address, so ssh verifies the host key pinned at creation
// (hostkeys.go). Port forwards carry no such check: a neighbour on a VM's
// address receives that VM's forwarded connections.
func (b *Backend) Dial(ctx context.Context, name string, port int) (backend.Conn, error) {
	m, err := b.machine(ctx, name)
	if err != nil {
		return nil, err
	}
	if m.state() != vm.StateRunning || m.addr() == "" {
		return nil, fmt.Errorf("VM '%s' is %s (skiff start %s)", name, m.state(), name)
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
