package guest

import (
	"fmt"
	"path"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/config"
)

// The broker relay gives clients that only speak HTTP over a Unix socket
// (gh with http_unix_socket) a path to the broker: systemd listens on
// config.BrokerGuestSocket and systemd-socket-proxyd forwards each
// connection to the tunnel port. The socket exists for as long as any cap is
// granted; without an open `cracklet ssh` its connections are simply refused.
const (
	RelaySocketUnit  = "/etc/systemd/system/cracklet-broker.socket"
	RelayServiceUnit = "/etc/systemd/system/cracklet-broker.service"
)

// asRootOnSystemd guards every systemctl call: the guest runs the scripts as
// root under systemd, tests and CI runners do neither.
const asRootOnSystemd = `[ "$(id -u)" = 0 ] && [ -d /run/systemd/system ]`

func relaySocket() string {
	return fmt.Sprintf(`[Unit]
Description=cracklet broker on a Unix socket

[Socket]
ListenStream=%s
SocketMode=0600
DirectoryMode=0755
RemoveOnStop=yes

[Install]
WantedBy=sockets.target
`, config.BrokerGuestSocket)
}

func relayService() string {
	return fmt.Sprintf(`[Unit]
Description=cracklet broker relay to the SSH tunnel
Requires=cracklet-broker.socket
After=cracklet-broker.socket

[Service]
ExecStart=/usr/lib/systemd/systemd-socket-proxyd 127.0.0.1:%d
`, config.BrokerGuestPort)
}

func isRelayUnit(p string) bool {
	return p == RelaySocketUnit || p == RelayServiceUnit
}

// writeRelay installs and starts the relay.
func writeRelay(b *strings.Builder) {
	writeFile(b, RelaySocketUnit, "0644", []byte(relaySocket()))
	writeFile(b, RelayServiceUnit, "0644", []byte(relayService()))
	b.WriteString("if " + asRootOnSystemd + "; then systemctl daemon-reload && systemctl --quiet enable --now " + path.Base(RelaySocketUnit) + "; fi\n")
}

// stopRelay stops the relay before its units are removed. It is best effort:
// a unit someone already disabled or deleted must not fail the revoke, and
// the files go away regardless.
func stopRelay(b *strings.Builder) {
	b.WriteString("if " + asRootOnSystemd + "; then systemctl disable --now " +
		path.Base(RelaySocketUnit) + " " + path.Base(RelayServiceUnit) + " >/dev/null 2>&1 || true; fi\n")
}
