package container

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// dialAttempt bounds one connection attempt while a machine boots; a SYN to
// an address that is not up yet would otherwise block for the OS timeout.
const dialAttempt = 2 * time.Second

// errStoppedWhileBooting reports a deliberate `cracklet stop` during a boot;
// the VM is kept, its disk included.
var errStoppedWhileBooting = errors.New("stopped while it booted")

// waitForSSH polls until the machine has an address and sshd accepts. A
// machine that falls back to stopped after it was seen running has crashed
// and is reported at once; one whose record says it was stopped on purpose is
// reported at once as well, whenever that happens.
func (b *Backend) waitForSSH(ctx context.Context, name string) (vm.Info, error) {
	deadline := time.Now().Add(b.sshWait)
	var lastErr error
	seenRunning := false
	for {
		m, err := b.machine(ctx, name)
		if err != nil {
			return vm.Info{}, err
		}
		if r, ok, err := b.loadRecord(name); err == nil && ok && !r.Running {
			return vm.Info{}, fmt.Errorf("%s %w", name, errStoppedWhileBooting)
		}
		switch {
		case m.state() == vm.StateRunning && m.addr() != "":
			seenRunning = true
			attempt, cancel := context.WithTimeout(ctx, dialAttempt)
			conn, err := b.dial(attempt, net.JoinHostPort(m.addr(), "22"))
			cancel()
			if err == nil {
				_ = conn.Close()
				return b.describe(ctx, name)
			}
			lastErr = err
		case seenRunning && m.state() == vm.StateStopped:
			return vm.Info{}, fmt.Errorf("%s stopped while booting (container machine logs %s)", name, name)
		default:
			lastErr = fmt.Errorf("machine is %s", m.Status)
		}
		if time.Now().After(deadline) {
			return vm.Info{}, fmt.Errorf("%s did not answer on SSH within %s (last: %v; see 'container machine logs %s')", name, b.sshWait, lastErr, name)
		}
		select {
		case <-ctx.Done():
			return vm.Info{}, ctx.Err()
		case <-time.After(b.poll):
		}
	}
}

// restoreMachines boots the VMs that were running before the service went
// down. One bad machine or record does not block the rest; "restarted"
// means the boot was requested, the first command on the VM waits for sshd.
func (b *Backend) restoreMachines(ctx context.Context) error {
	names, err := b.recordNames()
	if err != nil {
		return err
	}
	machines, err := b.machines(ctx)
	if err != nil {
		return err
	}
	var started, failed []string
	for _, name := range names {
		r, _, err := b.loadRecord(name)
		if err != nil {
			if errors.Is(err, errCorruptRecord) {
				b.statusf("  [WARN] %v\n", err)
				continue
			}
			return err
		}
		m, ok := machines[name]
		if !r.Running || !ok || m.state() == vm.StateRunning {
			continue
		}
		if _, err := b.runLong(ctx, "machine", "run", "--name", name, "--root", "--", "true"); err != nil {
			failed = append(failed, name)
			continue
		}
		started = append(started, name)
	}
	if len(started) > 0 {
		b.statusf("==> Restarted microVMs: %s\n", strings.Join(started, ", "))
	}
	for _, name := range failed {
		b.statusf("  [WARN] %s did not come back; run 'cracklet start %s'\n", name, name)
	}
	return nil
}
