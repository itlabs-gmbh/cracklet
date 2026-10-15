package container

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// machine is one entry of `container machine ls --format json`.
type machine struct {
	ID     string `json:"id"`
	IP     string `json:"ipAddress"`
	Status string `json:"status"`
	CPUs   int    `json:"cpus"`
	Memory int64  `json:"memory"`
}

func (m machine) state() string {
	switch m.Status {
	case "running":
		return vm.StateRunning
	case "stopped":
		return vm.StateStopped
	}
	return m.Status
}

// machines lists every machine the service knows.
func (b *Backend) machines(ctx context.Context) (map[string]machine, error) {
	out, err := b.run(ctx, "machine", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	var list []machine
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("parse machine list: %w", err)
	}
	byName := make(map[string]machine, len(list))
	for _, m := range list {
		byName[m.ID] = m
	}
	return byName, nil
}

// List implements backend.Backend: the machines cracklet created (it keeps a
// record for each), in record order. A record whose machine is gone is
// dropped, so `ls` never shows a VM that `container machine rm` removed.
func (b *Backend) List(ctx context.Context) ([]vm.Info, error) {
	names, err := b.recordNames()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return []vm.Info{}, nil
	}
	machines, err := b.machines(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]vm.Info, 0, len(names))
	for _, name := range names {
		m, ok := machines[name]
		if !ok {
			if err := b.removeRecord(name); err != nil {
				return nil, err
			}
			continue
		}
		r, _, err := b.loadRecord(name)
		if err != nil {
			return nil, err
		}
		infos = append(infos, infoFrom(m, r))
	}
	return infos, nil
}

// Create implements backend.Backend. `machine create` boots the machine; the
// first `machine run` right after it fails in container 1.5 (ENODEV after
// ~11 s and a reboot), so readiness is judged by the address `machine ls`
// reports and sshd accepting, not by running a command.
func (b *Backend) Create(ctx context.Context, spec vm.Spec, createdAt time.Time) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	defer release()
	machines, err := b.machines(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	name := spec.Name
	if name == "" {
		records, err := b.recordNames()
		if err != nil {
			return vm.Info{}, err
		}
		taken := make(map[string]bool, len(machines)+len(records))
		for n := range machines {
			taken[n] = true
		}
		for _, n := range records {
			taken[n] = true
		}
		name = freeName(taken)
	} else if _, exists := machines[name]; exists {
		return vm.Info{}, fmt.Errorf("VM '%s' already exists", name)
	}
	rec := record{Owner: spec.Owner, Slot: spec.Slot, Profile: spec.ProfileName(), Running: true}
	if spec.Owner != "" {
		created := createdAt.UTC()
		rec.CreatedAt = &created
	}
	if err := b.saveRecord(name, rec); err != nil {
		return vm.Info{}, err
	}
	b.statusf("==> Creating %s (%d vCPUs, %d MiB)\n", name, spec.VCPUs, spec.MemMiB)
	_, err = b.run(ctx, "machine", "create", ImageRef, "--name", name,
		"--cpus", strconv.Itoa(spec.VCPUs), "--memory", strconv.Itoa(spec.MemMiB)+"M", "--home-mount", "none")
	if err != nil {
		_ = b.removeRecord(name)
		return vm.Info{}, err
	}
	info, err := b.waitForSSH(ctx, name)
	if err != nil {
		_, _ = b.run(context.WithoutCancel(ctx), "machine", "rm", name)
		_ = b.removeRecord(name)
		return vm.Info{}, err
	}
	return info, nil
}

// Start implements backend.Backend. `machine run` boots a stopped machine.
func (b *Backend) Start(ctx context.Context, name string) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	defer release()
	m, err := b.machine(ctx, name)
	if err != nil {
		return vm.Info{}, err
	}
	if m.state() != vm.StateRunning {
		b.statusf("==> Booting %s\n", name)
		if _, err := b.run(ctx, "machine", "run", "--name", name, "--root", "--", "true"); err != nil {
			return vm.Info{}, err
		}
	}
	info, err := b.waitForSSH(ctx, name)
	if err != nil {
		return vm.Info{}, err
	}
	return info, b.setRunning(name, true)
}

// Stop implements backend.Backend.
func (b *Backend) Stop(ctx context.Context, name string) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	defer release()
	if _, err := b.machine(ctx, name); err != nil {
		return vm.Info{}, err
	}
	if _, err := b.run(ctx, "machine", "stop", name); err != nil {
		return vm.Info{}, err
	}
	if err := b.setRunning(name, false); err != nil {
		return vm.Info{}, err
	}
	return b.describe(ctx, name)
}

// Remove implements backend.Backend. With a guard, a VM whose record no
// longer matches owner and creation time is a different VM under the same
// name: it is left alone and reported as gone, like the Lima agent does.
func (b *Backend) Remove(ctx context.Context, name string, guard *backend.RemoveGuard) (bool, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	rec, ok, err := b.loadRecord(name)
	if err != nil {
		return false, err
	}
	machines, err := b.machines(ctx)
	if err != nil {
		return false, err
	}
	if _, exists := machines[name]; !exists {
		if guard == nil && !ok {
			return false, fmt.Errorf("VM '%s' does not exist", name)
		}
		return true, b.removeRecord(name)
	}
	if guard != nil && (rec.Owner != guard.Owner || !sameTime(rec.CreatedAt, guard.CreatedAt)) {
		return true, nil
	}
	if _, err := b.run(ctx, "machine", "rm", name); err != nil {
		return false, err
	}
	return false, b.removeRecord(name)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// Forward implements backend.Backend; forwards arrive with the relay in a
// later phase (Capabilities.Forwards is false, so app refuses them earlier).
func (b *Backend) Forward(ctx context.Context, name string, _ []vm.Forward) (vm.Info, error) {
	return vm.Info{}, b.noForwards(ctx, name)
}

// Unforward implements backend.Backend.
func (b *Backend) Unforward(ctx context.Context, name string, _ []int) (vm.Info, error) {
	return vm.Info{}, b.noForwards(ctx, name)
}

func (b *Backend) noForwards(ctx context.Context, name string) error {
	hint := ""
	if m, err := b.machine(ctx, name); err == nil && m.IP != "" {
		hint = fmt.Sprintf("; the Mac reaches %s directly at %s:PORT", name, m.IP)
	}
	return fmt.Errorf("port forwards are not supported on the container backend yet%s", hint)
}

// machine looks one of cracklet's machines up.
func (b *Backend) machine(ctx context.Context, name string) (machine, error) {
	if _, ok, err := b.loadRecord(name); err != nil {
		return machine{}, err
	} else if !ok {
		return machine{}, fmt.Errorf("VM '%s' does not exist", name)
	}
	machines, err := b.machines(ctx)
	if err != nil {
		return machine{}, err
	}
	m, ok := machines[name]
	if !ok {
		_ = b.removeRecord(name)
		return machine{}, fmt.Errorf("VM '%s' does not exist", name)
	}
	return m, nil
}

func (b *Backend) describe(ctx context.Context, name string) (vm.Info, error) {
	m, err := b.machine(ctx, name)
	if err != nil {
		return vm.Info{}, err
	}
	r, _, err := b.loadRecord(name)
	if err != nil {
		return vm.Info{}, err
	}
	return infoFrom(m, r), nil
}

// waitForSSH polls until the machine has an address and sshd accepts.
func (b *Backend) waitForSSH(ctx context.Context, name string) (vm.Info, error) {
	deadline := time.Now().Add(b.sshWait)
	for {
		m, err := b.machine(ctx, name)
		if err != nil {
			return vm.Info{}, err
		}
		if m.IP != "" && m.state() == vm.StateRunning {
			if conn, err := b.dial(ctx, net.JoinHostPort(m.IP, "22")); err == nil {
				_ = conn.Close()
				r, _, err := b.loadRecord(name)
				if err != nil {
					return vm.Info{}, err
				}
				return infoFrom(m, r), nil
			}
		}
		if time.Now().After(deadline) {
			return vm.Info{}, fmt.Errorf("%s did not answer on SSH within %s (container machine logs %s)", name, b.sshWait, name)
		}
		select {
		case <-ctx.Done():
			return vm.Info{}, ctx.Err()
		case <-time.After(b.poll):
		}
	}
}

// restoreMachines boots the VMs that were running before the service went
// down. Failures are collected so one bad machine does not block the rest.
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
			return err
		}
		m, ok := machines[name]
		if !r.Running || !ok || m.state() == vm.StateRunning {
			continue
		}
		if _, err := b.run(ctx, "machine", "run", "--name", name, "--root", "--", "true"); err != nil {
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
