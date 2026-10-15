package app

import (
	"context"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
)

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
	a.printf("\nReady. Next steps:\n  cracklet new            # boot a microVM\n  cracklet ssh vm1        # open a shell\n\n"+
		"Optional, to use plain 'ssh vm1.cracklet', add this line to ~/.ssh/config:\n  Include %s\n", a.paths.SSHConfigPath())
	return nil
}

// Doctor prints the host and runtime state without changing anything.
func (a *App) Doctor(ctx context.Context) error {
	a.printf("backend: %s\n", a.backend.Name())
	return a.backend.Doctor(ctx)
}
