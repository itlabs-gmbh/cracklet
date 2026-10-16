package app

import (
	"context"
)

// Prepare makes the host ready: runtime, guest image, keys, ssh config.
// Every step is idempotent so the command can be re-run after a failure.
func (a *App) Prepare(ctx context.Context) error {
	if err := a.backend.Prepare(ctx); err != nil {
		return err
	}
	if err := a.writeSSHConfig(); err != nil {
		return err
	}
	a.printf("\nReady. Next steps:\n  skiff new            # boot a VM\n  skiff ssh vm1        # open a shell\n\n"+
		"Optional, to use plain 'ssh vm1.skiff', add this line to ~/.ssh/config:\n  Include %s\n", a.paths.SSHConfigPath())
	return nil
}

// Doctor prints the host and runtime state without changing anything.
func (a *App) Doctor(ctx context.Context) error {
	a.printf("backend: %s\n", a.backend.Name())
	return a.backend.Doctor(ctx)
}
