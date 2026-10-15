package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/itlabs-gmbh/skiff/internal/backend"
)

// CheckBackendSwitch refuses to move to the active backend while the stored
// one still owns VMs or their host state: gc would take the grants and tokens
// of VMs it can no longer list for leftovers and remove them. Parallel
// installations use separate SKIFF_HOMEs.
func (a *App) CheckBackendSwitch(ctx context.Context) error {
	previous, err := backend.LoadName(a.paths)
	if err != nil || previous == a.backend.Name() {
		return err
	}
	names, err := a.grantStore().Names()
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("the %s backend still has grants for %s; remove those VMs ('skiff rm') or run 'skiff gc' before switching to %s, or use a separate SKIFF_HOME",
			previous, strings.Join(names, ", "), a.backend.Name())
	}
	old := a.backendNamed(previous)
	if err := old.Alive(ctx); err != nil {
		return nil // nothing of it is running, so nothing can be stranded by surprise
	}
	vms, err := old.List(ctx)
	if err != nil {
		return fmt.Errorf("list the VMs of the %s backend before switching: %w", previous, err)
	}
	if len(vms) > 0 {
		return fmt.Errorf("the %s backend still has VMs (%s); remove them with 'skiff rm' before switching to %s, or use a separate SKIFF_HOME",
			previous, vmNames(vms), a.backend.Name())
	}
	return nil
}

func vmNames(vms []VMInfo) string {
	names := make([]string, 0, len(vms))
	for _, v := range vms {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

// RememberBackend stores the active backend as the one later commands use.
func (a *App) RememberBackend() error {
	previous, err := backend.LoadName(a.paths)
	if err != nil {
		return err
	}
	if err := backend.SaveName(a.paths, a.backend.Name()); err != nil {
		return err
	}
	if previous != a.backend.Name() {
		a.printf("==> skiff now uses the %s backend\n", a.backend.Name())
	}
	return nil
}

// PrepareOptions and SizeFlags are the backend's; the aliases keep the CLI
// and tests readable while both backends exist.
type (
	PrepareOptions = backend.PrepareOptions
	SizeFlags      = backend.SizeFlags
)

// Prepare makes the host ready: backend runtime, images, keys, ssh config.
// Every step is idempotent so the command can be re-run after a failure.
func (a *App) Prepare(ctx context.Context, o PrepareOptions) error {
	if err := a.backend.Prepare(ctx, o); err != nil {
		return err
	}
	if err := a.writeSSHConfig(); err != nil {
		return err
	}
	a.printf("\nReady. Next steps:\n  skiff new            # boot a microVM\n  skiff ssh vm1        # open a shell\n\n"+
		"Optional, to use plain 'ssh vm1.skiff', add this line to ~/.ssh/config:\n  Include %s\n", a.paths.SSHConfigPath())
	return nil
}

// Doctor prints the host and runtime state without changing anything.
func (a *App) Doctor(ctx context.Context) error {
	a.printf("backend: %s\n", a.backend.Name())
	return a.backend.Doctor(ctx)
}
