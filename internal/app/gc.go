package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// GCOptions selects the microVMs `cracklet gc` removes. Only VMs with an
// owner are ever collected; VMs made by hand have none. Without Owner and
// OlderThan no VM is selected and gc only prunes host-side leftovers.
type GCOptions struct {
	// Owner restricts gc to the VMs of one owner.
	Owner string
	// OlderThan restricts gc to VMs created at least that long ago; a VM
	// whose creation time is unknown is never old enough.
	OlderThan time.Duration
	// Keep names VMs that stay regardless, e.g. the ones a tool still tracks.
	Keep []string
	// DryRun reports what would be removed without removing anything.
	DryRun bool
}

// GCResult is what gc removed, or would remove in a dry run.
type GCResult struct {
	VMs []VMInfo `json:"vms"`
	// HostState names VMs that no longer exist but still had grants or a
	// token on the Mac.
	HostState []string `json:"host_state"`
	DryRun    bool     `json:"dry_run"`
}

func (o GCOptions) validate() error {
	if o.Owner != "" {
		if err := vm.ValidateLabel("owner", o.Owner); err != nil {
			return err
		}
	}
	if o.OlderThan < 0 {
		return fmt.Errorf("--older-than must not be negative, got %s", o.OlderThan)
	}
	for _, name := range o.Keep {
		if err := vm.ValidateName(name); err != nil {
			return fmt.Errorf("--keep: %w", err)
		}
	}
	return nil
}

// stateCreating is what the agent reports while `cracklet new` builds a VM.
const stateCreating = "creating"

func (o GCOptions) collects(v VMInfo, now time.Time) bool {
	if v.Owner == "" || v.State == stateCreating || (o.Owner == "" && o.OlderThan == 0) {
		return false
	}
	if o.Owner != "" && v.Owner != o.Owner {
		return false
	}
	if slices.Contains(o.Keep, v.Name) {
		return false
	}
	if o.OlderThan > 0 && (v.CreatedAt == nil || now.Sub(*v.CreatedAt) < o.OlderThan) {
		return false
	}
	return true
}

// GC removes the owned microVMs opts selects and then the host-side state
// of VMs that no longer exist, including the ones it just removed. A failed
// removal does not stop the others; the errors are returned together with
// what was removed.
func (a *App) GC(ctx context.Context, opts GCOptions) (GCResult, error) {
	if err := opts.validate(); err != nil {
		return GCResult{}, err
	}
	if err := a.readyForAgent(ctx); err != nil {
		return GCResult{}, err
	}
	vms, err := a.agentVMs(ctx)
	if err != nil {
		return GCResult{}, err
	}
	res := GCResult{VMs: []VMInfo{}, HostState: []string{}, DryRun: opts.DryRun}
	var errs []error
	now := a.now()
	// listed are the VMs known to exist; removed and gone ones leave it, so
	// their host state is pruned below with a re-check under the grant locks
	// that keeps the state of a VM recreated under the same name meanwhile.
	listed := make(map[string]bool, len(vms))
	collected := map[string]bool{}
	for _, v := range vms {
		listed[v.Name] = true
		if !opts.collects(v, now) {
			continue
		}
		if !opts.DryRun {
			gone, err := a.removeListed(ctx, v)
			if err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", v.Name, err))
				continue
			}
			delete(listed, v.Name)
			if gone {
				continue // removed by someone else since the listing
			}
		}
		a.printf("%s %s\n", verb(opts.DryRun), a.describeOwned(v, now))
		res.VMs = append(res.VMs, v)
		collected[v.Name] = true
	}
	pruned, err := a.pruneHostState(ctx, listed, opts.DryRun)
	if err != nil {
		errs = append(errs, err)
	}
	for _, name := range pruned {
		if collected[name] {
			continue // its state went with the VM reported above
		}
		a.printf("%s host state of %s\n", verb(opts.DryRun), name)
		res.HostState = append(res.HostState, name)
	}
	if len(res.VMs) == 0 && len(res.HostState) == 0 && len(errs) == 0 {
		a.printf("nothing to collect\n")
	}
	return res, errors.Join(errs...)
}

// removeListed removes a VM only if it is still the one gc listed: the
// agent compares owner and creation time under its lock, so a VM removed
// and recreated under the same name in the meantime survives. gone reports
// that the VM no longer existed. The host state is left to pruneHostState:
// removing it here could hit the grants of a VM recreated right after.
func (a *App) removeListed(ctx context.Context, v VMInfo) (gone bool, err error) {
	created := "-"
	if v.CreatedAt != nil {
		created = v.CreatedAt.UTC().Format(time.RFC3339)
	}
	out, err := a.agentOutput(ctx, "rm", v.Name, v.Owner, created)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "gone", nil
}

// pruneHostState removes the grants and tokens of VMs the agent does not
// list. Store.Prune re-lists under the VMs' locks before removing, so a VM
// created and granted after the first listing keeps its state.
func (a *App) pruneHostState(ctx context.Context, listed map[string]bool, dryRun bool) ([]string, error) {
	store := a.grantStore()
	names, err := store.Names()
	if err != nil {
		return nil, err
	}
	var orphans []string
	for _, name := range names {
		if !listed[name] {
			orphans = append(orphans, name)
		}
	}
	if dryRun || len(orphans) == 0 {
		return orphans, nil
	}
	return store.Prune(orphans, func() (map[string]bool, error) {
		vms, err := a.agentVMs(ctx)
		if err != nil {
			return nil, err
		}
		live := make(map[string]bool, len(vms))
		for _, v := range vms {
			live[v.Name] = true
		}
		return live, nil
	})
}

// pruneVanished removes the host state of every VM that does not exist, so a
// VM about to be created under a reused name (given, or picked by the agent)
// starts without the grants of its predecessor: the broker authorises by
// name and would serve them as soon as the VM runs.
func (a *App) pruneVanished(ctx context.Context) error {
	names, err := a.grantStore().Names()
	if err != nil || len(names) == 0 {
		return err // nothing to prune spares the agent call
	}
	vms, err := a.agentVMs(ctx)
	if err != nil {
		return err
	}
	listed := make(map[string]bool, len(vms))
	for _, v := range vms {
		listed[v.Name] = true
	}
	_, err = a.pruneHostState(ctx, listed, false)
	return err
}

// describeOwned renders a VM gc acts on, e.g. "paseo-1 (owner paseo, slot 1, 3h old)".
func (a *App) describeOwned(v VMInfo, now time.Time) string {
	parts := []string{"owner " + v.Owner}
	if v.Slot != "" {
		parts = append(parts, "slot "+v.Slot)
	}
	if v.CreatedAt != nil {
		parts = append(parts, HumanAge(now.Sub(*v.CreatedAt))+" old")
	}
	return fmt.Sprintf("%s (%s)", v.Name, strings.Join(parts, ", "))
}

func verb(dryRun bool) string {
	if dryRun {
		return "would remove"
	}
	return "removed"
}

// HumanAge renders a duration in its largest whole unit: 45s, 12m, 5h, 3d.
// Hours are kept up to two days so that "yesterday" stays distinguishable.
func HumanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d/time.Second), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
