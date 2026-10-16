package lima

import (
	"context"
	"fmt"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/config"
)

// SSHProxyCommand implements backend.Backend: microVMs sit behind the Lima VM,
// so ssh hops through it with Lima's own ssh_config. The Lima backend is not
// migrated to `skiff _dial`; its ProxyCommand goes away with the backend.
func (b *Backend) SSHProxyCommand() string {
	return fmt.Sprintf("ssh -F %s -W %%h:%%p lima-%s", b.paths.LimaSSHConfig(config.Instance), config.Instance)
}

// Dial implements backend.Backend. Nothing on the Lima path needs it: ssh
// reaches the guests through the ProxyCommand above and forwards are Lima
// port forwards.
func (b *Backend) Dial(context.Context, string, int) (backend.Conn, error) {
	return nil, fmt.Errorf("lima backend: %w", backend.ErrNoDial)
}

// KnownHostsFile implements backend.Backend. Every Lima guest boots the same
// image and so presents the same host key; a firewall per guest keeps a guest
// from posing as another, so there is nothing to pin.
func (b *Backend) KnownHostsFile() string { return "" }
