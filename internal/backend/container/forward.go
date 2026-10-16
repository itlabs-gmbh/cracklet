package container

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/vm"
)

// forwardTarget is where a host port leads.
type forwardTarget struct {
	VM    string
	Guest int
}

// Forward implements backend.Backend. `container machine` cannot publish
// ports, so a forward is a line in the VM's record that the relay (relay.go)
// serves on 127.0.0.1:HOST. A host port belongs to one VM; forwarding it
// again on the same VM replaces the guest port. The relay is
// started (or kicked, if it idled out) once the record is written.
func (b *Backend) Forward(ctx context.Context, name string, forwards []vm.Forward) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	defer release()
	rec, err := b.recordOf(ctx, name)
	if err != nil {
		return vm.Info{}, err
	}
	owners, err := b.forwardTargets()
	if err != nil {
		return vm.Info{}, err
	}
	for _, f := range forwards {
		if owner, taken := owners[f.Host]; taken && owner.VM != name {
			return vm.Info{}, fmt.Errorf("host port %d is already forwarded to VM '%s'", f.Host, owner.VM)
		}
	}
	rec.Forwards = mergeForwards(rec.Forwards, forwards)
	if err := b.saveRecord(name, rec); err != nil {
		return vm.Info{}, err
	}
	if err := b.ensureRelay(ctx); err != nil {
		return vm.Info{}, fmt.Errorf("the forward is stored, but the relay that serves it did not start: %w", err)
	}
	return b.describe(ctx, name)
}

// Unforward implements backend.Backend; a port that is not forwarded is left
// alone. The relay drops the listener on its next look at the records.
func (b *Backend) Unforward(ctx context.Context, name string, hostPorts []int) (vm.Info, error) {
	release, err := b.lock(ctx)
	if err != nil {
		return vm.Info{}, err
	}
	defer release()
	rec, err := b.recordOf(ctx, name)
	if err != nil {
		return vm.Info{}, err
	}
	drop := make(map[int]bool, len(hostPorts))
	for _, p := range hostPorts {
		drop[p] = true
	}
	kept := make([]vm.Forward, 0, len(rec.Forwards))
	for _, f := range rec.Forwards {
		if !drop[f.Host] {
			kept = append(kept, f)
		}
	}
	rec.Forwards = kept
	if err := b.saveRecord(name, rec); err != nil {
		return vm.Info{}, err
	}
	return b.describe(ctx, name)
}

// recordOf loads the record of one of skiff's existing machines for a
// change; a corrupt record cannot be changed, only removed.
func (b *Backend) recordOf(ctx context.Context, name string) (record, error) {
	if _, err := b.machine(ctx, name); err != nil {
		return record{}, err
	}
	rec, _, err := b.loadRecord(name)
	if err != nil {
		return record{}, err
	}
	return rec, nil
}

// mergeForwards returns existing with forwards added; a host port already in
// existing takes the new guest port in place.
func mergeForwards(existing, forwards []vm.Forward) []vm.Forward {
	merged := make([]vm.Forward, 0, len(existing)+len(forwards))
	replaced := map[int]vm.Forward{}
	for _, f := range forwards {
		replaced[f.Host] = vm.Forward{Host: f.Host, Guest: f.Guest}
	}
	for _, f := range existing {
		if r, ok := replaced[f.Host]; ok {
			merged = append(merged, r)
			delete(replaced, f.Host)
			continue
		}
		merged = append(merged, f)
	}
	for _, f := range forwards {
		if r, ok := replaced[f.Host]; ok {
			merged = append(merged, r)
			delete(replaced, f.Host)
		}
	}
	return merged
}

// forwardTargets collects every recorded forward by host port. Records that
// cannot be read are skipped: the relay must keep serving the others.
func (b *Backend) forwardTargets() (map[int]forwardTarget, error) {
	names, err := b.recordNames()
	if err != nil {
		return nil, err
	}
	targets := map[int]forwardTarget{}
	for _, name := range names {
		rec, _, err := b.loadRecord(name)
		if err != nil {
			if errors.Is(err, errCorruptRecord) {
				continue
			}
			return nil, err
		}
		for _, f := range rec.Forwards {
			targets[f.Host] = forwardTarget{VM: name, Guest: f.Guest}
		}
	}
	return targets, nil
}

// probeTimeout bounds the check whether the relay listens on a host port.
const probeTimeout = 250 * time.Millisecond

// forwardStates reports the recorded forwards with their state: active when
// the VM runs and the relay listens on the Mac side, inactive otherwise.
func (b *Backend) forwardStates(ctx context.Context, m machine, rec record) []vm.Forward {
	states := make([]vm.Forward, 0, len(rec.Forwards))
	for _, f := range rec.Forwards {
		state := "inactive"
		if m.state() == vm.StateRunning && b.listening(ctx, f.Host) {
			state = "active"
		}
		states = append(states, vm.Forward{Host: f.Host, Guest: f.Guest, State: state})
	}
	return states
}

// listening reports whether something accepts on 127.0.0.1:port.
func (b *Backend) listening(ctx context.Context, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := b.dial(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
