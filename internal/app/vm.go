package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// cleanupTimeout bounds the best-effort removal of a VM whose creation was
// interrupted; it may have to wait for the agent's lock first.
const cleanupTimeout = 2 * time.Minute

// VMInfo is what the agent reports about a microVM.
type VMInfo struct {
	Name     string       `json:"name"`
	Index    int          `json:"index"`
	IP       string       `json:"ip"`
	State    string       `json:"state"`
	VCPUs    int          `json:"vcpus"`
	MemMiB   int          `json:"mem_mib"`
	Profile  string       `json:"profile"`
	Forwards []vm.Forward `json:"forwards"`
	// Grants are the host-side grants of the VM (not reported by the agent).
	Grants []string `json:"grants,omitempty"`
	// Owner and Slot label VMs managed by a tool; empty for VMs made by hand.
	Owner string `json:"owner"`
	Slot  string `json:"slot"`
	// CreatedAt is nil for VMs created before cracklet recorded it.
	CreatedAt *time.Time `json:"created_at"`
}

// NewVM creates and boots a microVM.
func (a *App) NewVM(ctx context.Context, spec vm.Spec) (VMInfo, error) {
	if err := spec.Validate(); err != nil {
		return VMInfo{}, err
	}
	forwards := make([]vm.Forward, 0, len(spec.Forwards))
	for _, s := range spec.Forwards {
		f, _ := vm.ParseForward(s) // validated above
		forwards = append(forwards, f)
	}
	if err := a.checkHostPortsFree(forwards); err != nil {
		return VMInfo{}, err
	}
	release, err := a.holdLimaForMicroVM()
	if err != nil {
		return VMInfo{}, err
	}
	defer release()
	if err := a.readyForAgent(ctx); err != nil {
		return VMInfo{}, err
	}
	name := spec.Name
	if name == "" {
		name = "-" // the agent picks the first free vm<N>
	}
	disk, err := vm.NormalizeSize(spec.DiskSize())
	if err != nil {
		return VMInfo{}, err
	}
	if err := a.pruneVanished(ctx); err != nil {
		return VMInfo{}, fmt.Errorf("clear grants left by removed VMs: %w", err)
	}
	args := []string{"new", name, strconv.Itoa(spec.VCPUs), strconv.Itoa(spec.MemMiB), disk, spec.Mode(), spec.ProfileName()}
	if spec.Owner != "" { // a slot needs an owner, see Spec.Validate
		// gc measures ages with this clock; the Lima VM's may lag after sleep
		args = append(args, spec.Owner, orDash(spec.Slot), a.now().UTC().Format(time.RFC3339))
	}
	var out []byte
	started := false
	create := func() (err error) {
		// Ctrl-C while waiting for the lock: another `new` of this name
		// may hold it, and its VM must not be cleaned up as ours.
		if err := ctx.Err(); err != nil {
			return err
		}
		started = true
		out, err = a.agentOutput(ctx, args...)
		return err
	}
	// pruneVanished cleared leftovers before, but an earlier VM of this name
	// may have been removed only after that (by gc, whose own prune then sees
	// the new VM and keeps the state). A named VM is therefore created under
	// its grant lock and starts clean before a concurrent grant can save.
	if spec.Name != "" {
		err = a.grantStore().WithFreshState(spec.Name, create)
	} else {
		err = create()
	}
	if err != nil {
		if started && ctx.Err() != nil {
			a.cleanupInterrupted(spec.Name)
		}
		return VMInfo{}, err
	}
	info, err := parseVM(out)
	if err != nil {
		return VMInfo{}, err
	}
	if spec.Name == "" {
		// The agent picked the name, so it could not be locked in advance;
		// nobody can have granted the VM before learning its name.
		if err := a.grantStore().Remove(info.Name); err != nil {
			return info, fmt.Errorf("%s was created, but clearing grants left by an earlier VM of that name failed: %w", info.Name, err)
		}
	}
	a.printf("%s is running at %s\n  cracklet ssh %s\n  ssh %s.%s\n", info.Name, info.IP, info.Name, info.Name, config.Instance)
	if len(spec.Forwards) > 0 {
		if info, err = a.Forward(ctx, info.Name, spec.Forwards); err != nil {
			return info, err
		}
	}
	if len(spec.Grants) > 0 {
		set, err := a.Grant(ctx, info.Name, spec.Grants)
		if err != nil {
			return info, err
		}
		info.Grants = set.Caps()
	}
	return info, nil
}

// StartVM boots a stopped microVM.
func (a *App) StartVM(ctx context.Context, name string) (VMInfo, error) {
	release, err := a.holdLimaForMicroVM()
	if err != nil {
		return VMInfo{}, err
	}
	defer release()
	return a.lifecycle(ctx, "start", name)
}

// StopVM shuts a microVM down but keeps its disk.
func (a *App) StopVM(ctx context.Context, name string) (VMInfo, error) {
	return a.lifecycle(ctx, "stop", name)
}

func (a *App) lifecycle(ctx context.Context, action, name string) (VMInfo, error) {
	if err := vm.ValidateName(name); err != nil {
		return VMInfo{}, err
	}
	if err := a.readyForAgent(ctx); err != nil {
		return VMInfo{}, err
	}
	out, err := a.agentOutput(ctx, action, name)
	if err != nil {
		return VMInfo{}, err
	}
	info, err := parseVM(out)
	if err != nil {
		return VMInfo{}, err
	}
	a.printf("%s is %s\n", info.Name, info.State)
	return info, nil
}

// RemoveVM stops and deletes a microVM.
func (a *App) RemoveVM(ctx context.Context, name string) error {
	if err := vm.ValidateName(name); err != nil {
		return err
	}
	if err := a.readyForAgent(ctx); err != nil {
		return err
	}
	if err := a.removeVM(ctx, name); err != nil {
		return err
	}
	a.printf("%s removed\n", name)
	return nil
}

// removeVM deletes a VM and its host-side state; the agent must be ready.
func (a *App) removeVM(ctx context.Context, name string) error {
	if _, err := a.agentOutput(ctx, "rm", name); err != nil {
		return err
	}
	return a.grantStore().Remove(name)
}

// ListVMs returns all microVMs known to the agent.
func (a *App) ListVMs(ctx context.Context) ([]VMInfo, error) {
	if err := a.readyForAgent(ctx); err != nil {
		return nil, err
	}
	vms, err := a.agentVMs(ctx)
	if err != nil {
		return nil, err
	}
	store := a.grantStore()
	for i := range vms {
		set, err := store.Load(vms[i].Name)
		if err != nil {
			return nil, err
		}
		vms[i].Grants = set.Caps()
	}
	return vms, nil
}

// InspectVM returns a single microVM, including its host-side grants.
func (a *App) InspectVM(ctx context.Context, name string) (VMInfo, error) {
	if err := vm.ValidateName(name); err != nil {
		return VMInfo{}, err
	}
	return a.describe(ctx, name)
}

// agentVMs lists the VMs as the agent reports them, without grants.
func (a *App) agentVMs(ctx context.Context) ([]VMInfo, error) {
	out, err := a.agentOutput(ctx, "ls")
	if err != nil {
		return nil, err
	}
	var vms []VMInfo
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	return vms, nil
}

// orDash maps an unset optional agent argument to the agent's "-".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func parseVM(out []byte) (VMInfo, error) {
	var info VMInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return VMInfo{}, fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	return info, nil
}

// cleanupInterrupted removes a VM whose creation was cancelled (Ctrl-C). The
// guest agent keeps running after the connection drops and either finishes or
// rolls back on its own, so the removal is best-effort and waits for its lock.
func (a *App) cleanupInterrupted(name string) {
	if name == "" {
		a.printf("creation interrupted; check 'cracklet ls' and remove leftovers with 'cracklet rm'\n")
		return
	}
	a.printf("creation of %s interrupted, cleaning up\n", name)
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := a.agentOutput(ctx, "rm", name); err != nil {
		a.printf("  cleanup did not complete (%v); check 'cracklet ls'\n", err)
	}
}
