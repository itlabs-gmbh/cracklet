package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// addr is the bare address; 1.5.0 prints one, a later release might add /24.
func (m machine) addr() string {
	ip, _, _ := strings.Cut(m.IP, "/")
	return ip
}

// machines lists every machine the service knows.
func (b *Backend) machines(ctx context.Context) (map[string]machine, error) {
	out, err := b.run(ctx, "machine", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	var list []machine
	if len(strings.TrimSpace(string(out))) > 0 {
		if err := json.Unmarshal(out, &list); err != nil {
			return nil, fmt.Errorf("parse machine list: %w", err)
		}
	}
	byName := make(map[string]machine, len(list))
	for _, m := range list {
		byName[m.ID] = m
	}
	return byName, nil
}

// List implements backend.Backend: the machines cracklet created (it keeps a
// record for each), in record order. List is read-only; a record without a
// machine is skipped, not deleted, because it may belong to a VM another
// command is creating right now (pruneRecords handles leftovers under the
// lock). A corrupt record is reported once and the VM listed without metadata.
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
			continue
		}
		r, _, err := b.loadRecord(name)
		if errors.Is(err, errCorruptRecord) {
			b.statusf("  [WARN] %v; 'cracklet rm %s' removes the VM\n", err, name)
		} else if err != nil {
			return nil, err
		}
		infos = append(infos, b.info(ctx, m, r))
	}
	return infos, nil
}

// Create implements backend.Backend. `machine create` boots the machine; the
// first `machine run` right after it fails in container 1.5 (ENODEV after
// ~11 s and a reboot), so readiness is judged by the address `machine ls`
// reports and sshd accepting, not by running a command. The lock covers the
// API call only; waiting for sshd runs unlocked so other VMs are not held up.
func (b *Backend) Create(ctx context.Context, spec vm.Spec, createdAt time.Time) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	name, err := b.createLocked(ctx, spec, createdAt)
	release()
	if err != nil {
		return vm.Info{}, err
	}
	info, err := b.waitForSSH(ctx, name)
	if errors.Is(err, errStoppedWhileBooting) {
		return vm.Info{}, fmt.Errorf("%w and is kept; 'cracklet start %s' boots it", err, name)
	}
	if err == nil {
		err = b.pinHostKeys(ctx, name, info.IP)
	}
	if err != nil {
		return vm.Info{}, b.discard(ctx, name, err)
	}
	return info, nil
}

// createLocked records and creates the machine; it returns the chosen name.
func (b *Backend) createLocked(ctx context.Context, spec vm.Spec, createdAt time.Time) (string, error) {
	machines, err := b.machines(ctx)
	if err != nil {
		return "", err
	}
	if err := b.pruneRecords(machines); err != nil {
		return "", err
	}
	name := spec.Name
	if name == "" {
		records, err := b.recordNames()
		if err != nil {
			return "", err
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
		return "", fmt.Errorf("VM '%s' already exists", name)
	}
	rec := record{Owner: spec.Owner, Slot: spec.Slot, Profile: spec.ProfileName(), Running: true}
	if spec.Owner != "" {
		created := createdAt.UTC()
		rec.CreatedAt = &created
	}
	if err := b.saveRecord(name, rec); err != nil {
		return "", err
	}
	b.statusf("==> Creating %s (%d vCPUs, %d MiB)\n", name, spec.VCPUs, spec.MemMiB)
	if _, err := b.runLong(ctx, "machine", "create", ImageRef, "--name", name,
		"--cpus", strconv.Itoa(spec.VCPUs), "--memory", strconv.Itoa(spec.MemMiB)+"M", "--home-mount", "none"); err != nil {
		cctx, cancel := cleanupContext(ctx)
		defer cancel()
		return "", b.discardLocked(cctx, name, err) // the caller holds the lock
	}
	return name, nil
}

// cleanupTimeout bounds the removal of a failed creation, lock wait included,
// so a hanging `machine rm` cannot hold container.lock forever.
const cleanupTimeout = 2 * time.Minute

// cleanupContext survives Ctrl-C (the cleanup is the one thing that must still
// happen) but not forever.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// discard removes a VM whose creation failed; it takes the lock first.
func (b *Backend) discard(ctx context.Context, name string, err error) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	release, lockErr := b.lock(ctx)
	if lockErr != nil {
		return errors.Join(err, lockErr)
	}
	defer release()
	return b.discardLocked(ctx, name, err)
}

// discardLocked removes the machine (if the API created one) and the record
// of a failed creation and folds a failed cleanup into err, so the user learns
// about a leftover instead of finding an invisible machine later. A VM that
// `cracklet stop` stopped while it was still booting is kept: its record says
// so, and stop promises to keep the disk.
func (b *Backend) discardLocked(ctx context.Context, name string, err error) error {
	if rec, ok, recErr := b.loadRecord(name); recErr == nil && ok && !rec.Running {
		return fmt.Errorf("%w; %s was stopped meanwhile and is kept, 'cracklet start %s' boots it", err, name, name)
	}
	machines, lsErr := b.machines(ctx)
	if lsErr != nil {
		return errors.Join(err, lsErr)
	}
	if _, exists := machines[name]; exists {
		if _, rmErr := b.runLong(ctx, "machine", "rm", name); rmErr != nil {
			return errors.Join(err, fmt.Errorf("remove the half-created machine %s with 'container machine rm %s': %w", name, name, rmErr))
		}
	}
	return errors.Join(err, b.removeRecord(name))
}

// Start implements backend.Backend. `machine run` boots a stopped machine.
func (b *Backend) Start(ctx context.Context, name string) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	m, err := b.machine(ctx, name)
	if err == nil && m.state() != vm.StateRunning {
		b.statusf("==> Booting %s\n", name)
		_, err = b.runLong(ctx, "machine", "run", "--name", name, "--root", "--", "true")
	}
	if err == nil {
		err = b.setRunning(name, true)
	}
	release()
	if err != nil {
		return vm.Info{}, err
	}
	b.kickRelayFor(ctx, name)
	return b.waitForSSH(ctx, name)
}

// Stop implements backend.Backend. The record is updated first, so a stop
// that the API completed but cracklet could not record never resurrects the
// VM on the next restart of the service.
func (b *Backend) Stop(ctx context.Context, name string) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	defer release()
	if _, err := b.machine(ctx, name); err != nil {
		return vm.Info{}, err
	}
	if err := b.setRunning(name, false); err != nil {
		return vm.Info{}, err
	}
	if _, err := b.runLong(ctx, "machine", "stop", name); err != nil {
		if back := b.setRunning(name, true); back != nil {
			err = errors.Join(err, back)
		}
		return vm.Info{}, err
	}
	return b.describe(ctx, name)
}

// Remove implements backend.Backend. Only VMs cracklet created (those with a
// record) are removed: `cracklet rm foo` must never delete a machine the user
// made with `container machine create`. With a guard, a VM whose record no
// longer matches owner and creation time is a different VM under the same
// name: it is left alone and reported as gone, like the Lima agent does.
func (b *Backend) Remove(ctx context.Context, name string, guard *backend.RemoveGuard) (bool, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	rec, ok, err := b.loadRecord(name)
	if err != nil && !errors.Is(err, errCorruptRecord) {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("VM '%s' does not exist", name)
	}
	machines, err := b.machines(ctx)
	if err != nil {
		return false, err
	}
	if _, exists := machines[name]; !exists {
		return guard != nil, b.removeRecord(name)
	}
	if guard != nil && (rec.Owner != guard.Owner || !sameTime(rec.CreatedAt, guard.CreatedAt)) {
		return true, nil
	}
	if _, err := b.runLong(ctx, "machine", "rm", name); err != nil {
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

// machine looks one of cracklet's machines up (read-only).
func (b *Backend) machine(ctx context.Context, name string) (machine, error) {
	if _, ok, err := b.loadRecord(name); err != nil && !errors.Is(err, errCorruptRecord) {
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
	if err != nil && !errors.Is(err, errCorruptRecord) {
		return vm.Info{}, err
	}
	return b.info(ctx, m, r), nil
}

// info combines what `container` reports with the record, forwards and
// their observed state included.
func (b *Backend) info(ctx context.Context, m machine, r record) vm.Info {
	info := infoFrom(m, r)
	info.Forwards = b.forwardStates(ctx, m, r)
	return info
}
