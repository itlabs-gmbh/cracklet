package lima

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// Create implements backend.Backend.
func (b *Backend) Create(ctx context.Context, spec vm.Spec, createdAt time.Time) (vm.Info, error) {
	name := spec.Name
	if name == "" {
		name = "-" // the agent picks the first free vm<N>
	}
	disk, err := vm.NormalizeSize(spec.DiskSize())
	if err != nil {
		return vm.Info{}, err
	}
	args := []string{"new", name, strconv.Itoa(spec.VCPUs), strconv.Itoa(spec.MemMiB), disk, spec.Mode(), spec.ProfileName()}
	if spec.Owner != "" { // a slot needs an owner, see Spec.Validate
		// gc measures ages with the host clock; the Lima VM's may lag after sleep
		args = append(args, spec.Owner, orDash(spec.Slot), createdAt.UTC().Format(time.RFC3339))
	}
	out, err := b.agentOutput(ctx, args...)
	if err != nil {
		return vm.Info{}, err
	}
	return parseVM(out)
}

// Start implements backend.Backend.
func (b *Backend) Start(ctx context.Context, name string) (vm.Info, error) {
	return b.lifecycle(ctx, "start", name)
}

// Stop implements backend.Backend.
func (b *Backend) Stop(ctx context.Context, name string) (vm.Info, error) {
	return b.lifecycle(ctx, "stop", name)
}

func (b *Backend) lifecycle(ctx context.Context, action, name string) (vm.Info, error) {
	out, err := b.agentOutput(ctx, action, name)
	if err != nil {
		return vm.Info{}, err
	}
	return parseVM(out)
}

// Remove implements backend.Backend. With a guard the agent compares owner
// and creation time under its lock and answers "gone" when the VM listed by
// the caller no longer exists.
func (b *Backend) Remove(ctx context.Context, name string, guard *backend.RemoveGuard) (bool, error) {
	args := []string{"rm", name}
	if guard != nil {
		created := "-"
		if guard.CreatedAt != nil {
			created = guard.CreatedAt.UTC().Format(time.RFC3339)
		}
		args = append(args, guard.Owner, created)
	}
	out, err := b.agentOutput(ctx, args...)
	if err != nil {
		return false, err
	}
	return guard != nil && strings.TrimSpace(string(out)) == "gone", nil
}

// List implements backend.Backend.
func (b *Backend) List(ctx context.Context) ([]vm.Info, error) {
	out, err := b.agentOutput(ctx, "ls")
	if err != nil {
		return nil, err
	}
	var vms []vm.Info
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	return vms, nil
}

// Forward implements backend.Backend.
func (b *Backend) Forward(ctx context.Context, name string, forwards []vm.Forward) (vm.Info, error) {
	args := []string{"forward", name}
	for _, f := range forwards {
		args = append(args, f.Spec())
	}
	out, err := b.agentOutput(ctx, args...)
	if err != nil {
		return vm.Info{}, err
	}
	return parseVM(out)
}

// Unforward implements backend.Backend.
func (b *Backend) Unforward(ctx context.Context, name string, hostPorts []int) (vm.Info, error) {
	args := []string{"unforward", name}
	for _, p := range hostPorts {
		args = append(args, strconv.Itoa(p))
	}
	out, err := b.agentOutput(ctx, args...)
	if err != nil {
		return vm.Info{}, err
	}
	return parseVM(out)
}

// restoreResult is what `agent restore` reports.
type restoreResult struct {
	Started []string `json:"started"`
	Failed  []string `json:"failed"`
}

// restoreMicroVMs boots the microVMs that were running when the Lima VM
// stopped. The agent does so once per boot, so a second call only waits for
// the first (possibly the agent's boot unit) to finish.
func (b *Backend) restoreMicroVMs(ctx context.Context) error {
	out, err := b.agentOutput(ctx, "restore")
	if err != nil {
		return err
	}
	var r restoreResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	if len(r.Started) > 0 {
		b.statusf("==> Restarted microVMs: %s\n", strings.Join(r.Started, ", "))
	}
	for _, name := range r.Failed {
		b.statusf("  [WARN] %s did not come back; check 'cracklet inspect %s' and run 'cracklet start %s'\n", name, name, name)
	}
	return nil
}

// orDash maps an unset optional agent argument to the agent's "-".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func parseVM(out []byte) (vm.Info, error) {
	var info vm.Info
	if err := json.Unmarshal(out, &info); err != nil {
		return vm.Info{}, fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	return info, nil
}
