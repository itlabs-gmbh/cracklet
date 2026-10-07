package app

import (
	"context"
	"fmt"
	"os"

	"github.com/itlabs-gmbh/cracklet/internal/broker"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/grant"
	"github.com/itlabs-gmbh/cracklet/internal/secret"
)

// Tunnel starts (or reuses) the broker for a VM and returns the socket to
// forward plus a stop function. Tests replace it to avoid real sockets.
type Tunnel func(ctx context.Context, name string) (socket string, stop func(), err error)

// WithTunnel overrides how the broker is started (used by tests).
func WithTunnel(t Tunnel) Option {
	return func(a *App) { a.tunnel = t }
}

// sshExtraArgs returns the ssh options that attach the broker tunnel and, if
// granted, the SSH agent. It returns a stop function for the broker.
func (a *App) sshExtraArgs(ctx context.Context, name string) ([]string, func(), error) {
	set, err := a.grantStore().Load(name)
	if err != nil {
		return nil, nil, err
	}
	if len(set) == 0 {
		return nil, func() {}, nil
	}
	var args []string
	if set.Contains(grant.Grant{Cap: grant.SSHAgent}) {
		args = append(args, "-A")
	}
	if len(set.Caps()) == 1 && set.Caps()[0] == grant.SSHAgent {
		return args, func() {}, nil
	}
	socket, stop, err := a.tunnel(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	// Without ExitOnForwardFailure a squatter on the guest port would leave
	// the session running with no tunnel and only a warning.
	args = append(args, "-o", "ExitOnForwardFailure=yes", "-R", fmt.Sprintf("127.0.0.1:%d:%s", config.BrokerGuestPort, socket))
	return args, stop, nil
}

// startBroker is the production Tunnel: it serves the VM's broker on a Unix
// socket for as long as the ssh session lives. A broker left by another
// session of the same VM is reused.
func (a *App) startBroker(ctx context.Context, name string) (string, func(), error) {
	socket := a.paths.SocketPath(name)
	if broker.Alive(socket) {
		return socket, func() {}, nil
	}
	caps, err := a.loadCaps()
	if err != nil {
		return "", nil, err
	}
	data, err := a.templateData(name)
	if err != nil {
		return "", nil, err
	}
	audit, err := os.OpenFile(a.paths.AuditLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("open audit log: %w", err)
	}
	store := a.grantStore()
	b := &broker.Broker{
		VM:      name,
		Caps:    caps,
		Grants:  func() (grant.Set, error) { return store.Load(name) },
		Secrets: secret.NewResolver(a.r),
		Audit:   audit,
		Data:    data,
	}
	ln, err := broker.Listen(socket)
	if err != nil {
		_ = audit.Close()
		return "", nil, err
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := broker.Serve(serveCtx, ln, b.Handler()); err != nil {
			a.printf("broker for %s stopped: %v\n", name, err)
		}
	}()
	stop := func() {
		cancel()
		<-done
		b.Close()
		_ = audit.Close()
		_ = os.Remove(socket)
	}
	return socket, stop, nil
}
