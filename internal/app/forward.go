package app

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

const (
	// portWait is how long Forward waits for Lima to expose a new host port.
	portWait     = 15 * time.Second
	portInterval = 250 * time.Millisecond
)

// Forward adds port forwards to a microVM. With no specs it only reports the
// existing ones. Forwards persist across stop/start and are removed with the VM.
func (a *App) Forward(ctx context.Context, name string, specs []string) (VMInfo, error) {
	if err := vm.ValidateName(name); err != nil {
		return VMInfo{}, err
	}
	forwards := make([]vm.Forward, 0, len(specs))
	for _, s := range specs {
		f, err := vm.ParseForward(s)
		if err != nil {
			return VMInfo{}, err
		}
		forwards = append(forwards, f)
	}
	if err := a.readyForAgent(ctx); err != nil {
		return VMInfo{}, err
	}
	if len(forwards) == 0 {
		return a.describe(ctx, name)
	}
	if err := a.checkHostPortsFree(forwards); err != nil {
		return VMInfo{}, err
	}
	args := []string{"forward", name}
	for _, f := range forwards {
		args = append(args, f.Spec())
	}
	out, err := a.agentOutput(ctx, args...)
	if err != nil {
		return VMInfo{}, err
	}
	info, err := parseVM(out)
	if err != nil {
		return VMInfo{}, err
	}
	for _, f := range forwards {
		a.reportForward(ctx, info, f)
	}
	return info, nil
}

// Unforward removes forwards by host port.
func (a *App) Unforward(ctx context.Context, name string, hostPorts []int) (VMInfo, error) {
	if err := vm.ValidateName(name); err != nil {
		return VMInfo{}, err
	}
	if len(hostPorts) == 0 {
		return VMInfo{}, fmt.Errorf("specify at least one host port")
	}
	args := []string{"unforward", name}
	for _, p := range hostPorts {
		if p < 1 || p > vm.MaxPort {
			return VMInfo{}, fmt.Errorf("invalid port %d", p)
		}
		args = append(args, strconv.Itoa(p))
	}
	if err := a.readyForAgent(ctx); err != nil {
		return VMInfo{}, err
	}
	out, err := a.agentOutput(ctx, args...)
	if err != nil {
		return VMInfo{}, err
	}
	info, err := parseVM(out)
	if err != nil {
		return VMInfo{}, err
	}
	for _, p := range hostPorts {
		a.printf("removed forward localhost:%d\n", p)
	}
	return info, nil
}

// checkHostPortsFree refuses host ports that something on the Mac already
// uses: Lima could not bind them, and the forward would silently go nowhere.
func (a *App) checkHostPortsFree(forwards []vm.Forward) error {
	for _, f := range forwards {
		if a.portBusy(f.Host) {
			return fmt.Errorf("localhost:%d is already in use on this Mac; pick another host port (e.g. %d:%d)",
				f.Host, f.Host+1, f.Guest)
		}
	}
	return nil
}

// describe returns the agent's view of a single VM.
func (a *App) describe(ctx context.Context, name string) (VMInfo, error) {
	vms, err := a.ListVMs(ctx)
	if err != nil {
		return VMInfo{}, err
	}
	for _, v := range vms {
		if v.Name == name {
			return v, nil
		}
	}
	return VMInfo{}, fmt.Errorf("VM %q does not exist", name)
}

func (a *App) reportForward(ctx context.Context, info VMInfo, f vm.Forward) {
	if info.State != "running" {
		a.printf("localhost:%d -> %s:%d stored; it is applied when %s starts\n", f.Host, info.Name, f.Guest, info.Name)
		return
	}
	if a.probe(ctx, f.Host) {
		a.printf("localhost:%d -> %s:%d ready\n", f.Host, info.Name, f.Guest)
		return
	}
	a.printf("localhost:%d -> %s:%d set up in the Lima VM, but the Mac side is not reachable yet; "+
		"Lima usually exposes it within seconds\n", f.Host, info.Name, f.Guest)
}

// hostPortBusy reports whether 127.0.0.1:port already accepts connections.
func hostPortBusy(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), portInterval)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitForHostPort polls 127.0.0.1:port until it accepts a connection.
func waitForHostPort(ctx context.Context, port int) bool {
	deadline := time.Now().Add(portWait)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for time.Now().Before(deadline) && ctx.Err() == nil {
		conn, err := net.DialTimeout("tcp", addr, portInterval)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(portInterval)
	}
	return false
}
