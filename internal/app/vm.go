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
	Forwards []vm.Forward `json:"forwards"`
	// Grants are the host-side grants of the VM (not reported by the agent).
	Grants []string `json:"grants,omitempty"`
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
	if err := a.readyForAgent(ctx); err != nil {
		return VMInfo{}, err
	}
	name := spec.Name
	if name == "" {
		name = "-" // the agent picks the first free vm<N>
	}
	disk, err := vm.NormalizeSize(spec.Disk)
	if err != nil {
		return VMInfo{}, err
	}
	out, err := a.agentOutput(ctx, "new", name, strconv.Itoa(spec.VCPUs), strconv.Itoa(spec.MemMiB), disk, spec.Mode())
	if err != nil {
		if ctx.Err() != nil {
			a.cleanupInterrupted(spec.Name)
		}
		return VMInfo{}, err
	}
	info, err := parseVM(out)
	if err != nil {
		return VMInfo{}, err
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
		info.Grants = set.Strings()
	}
	return info, nil
}

// StartVM boots a stopped microVM.
func (a *App) StartVM(ctx context.Context, name string) (VMInfo, error) {
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
	if _, err := a.agentOutput(ctx, "rm", name); err != nil {
		return err
	}
	if err := a.grantStore().Remove(name); err != nil {
		return err
	}
	a.printf("%s removed\n", name)
	return nil
}

// ListVMs returns all microVMs known to the agent.
func (a *App) ListVMs(ctx context.Context) ([]VMInfo, error) {
	if err := a.readyForAgent(ctx); err != nil {
		return nil, err
	}
	out, err := a.agentOutput(ctx, "ls")
	if err != nil {
		return nil, err
	}
	var vms []VMInfo
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	store := a.grantStore()
	for i := range vms {
		set, err := store.Load(vms[i].Name)
		if err != nil {
			return nil, err
		}
		vms[i].Grants = set.Strings()
	}
	return vms, nil
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
