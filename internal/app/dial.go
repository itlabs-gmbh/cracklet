package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/sshcfg"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// Dial is the body of `cracklet _dial`, the ssh ProxyCommand of backends whose
// guests the Mac can reach directly: it connects to port on the microVM that
// host names (`<vm>.cracklet` as ssh passes %h, or a bare name) and relays
// stdin to the guest and the guest to stdout until either side closes.
func (a *App) Dial(ctx context.Context, host string, port int, stdin io.Reader, stdout io.Writer) error {
	name := sshcfg.VMName(host, config.Instance)
	if err := vm.ValidateName(name); err != nil {
		return err
	}
	if port < 1 || port > vm.MaxPort {
		return fmt.Errorf("invalid port %d", port)
	}
	conn, err := a.backend.Dial(ctx, name, port)
	if err != nil {
		return fmt.Errorf("connect to %s:%d: %w", name, port, err)
	}
	return relay(ctx, conn, stdin, stdout)
}

// relay copies stdin to conn and conn to stdout. EOF on stdin half-closes the
// connection so the guest sees it; the relay ends when the guest closes or ctx
// is cancelled. ssh treats a closed ProxyCommand as a dropped connection, so
// errors other than a plain close are reported.
func relay(ctx context.Context, conn backend.Conn, stdin io.Reader, stdout io.Writer) error {
	defer conn.Close()
	fromGuest := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, conn)
		fromGuest <- err
	}()
	toGuest := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, stdin)
		if err == nil {
			// Without the half-close the guest never sees EOF and a session
			// that waits for it (ssh's channel close) would hang.
			err = conn.CloseWrite()
		}
		toGuest <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-fromGuest:
		return ignoreClosed(err)
	case err := <-toGuest:
		if err != nil {
			return fmt.Errorf("relay to guest: %w", err)
		}
		// stdin is done; keep relaying what the guest still sends.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-fromGuest:
			return ignoreClosed(err)
		}
	}
}

func ignoreClosed(err error) error {
	if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("relay from guest: %w", err)
}

// Relay is the body of `cracklet _relay`, the launchd agent that serves the
// port forwards of backends whose guests the Mac reaches directly. It runs
// until ctx ends or the backend has no forwards left to serve.
func (a *App) Relay(ctx context.Context) error {
	relay, ok := a.backend.(backend.ForwardRelay)
	if !ok {
		return fmt.Errorf("the %s backend serves its port forwards itself; there is no relay to run", a.backend.Name())
	}
	return relay.ServeForwards(ctx)
}
