package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/itlabs-gmbh/skiff/internal/config"
)

// Spawner starts the forward relay for a state directory as a process that
// outlives the command; tests replace it.
type Spawner func(exe string, paths config.Paths) error

// WithRelaySpawner overrides how the relay process is started (used by tests).
func WithRelaySpawner(s Spawner) Option {
	return func(b *Backend) { b.spawn = s }
}

// ensureRelay starts the relay when forwards exist and no relay serves them.
// The relay holds relay.lock while it runs, so a free lock means there is
// none. Two commands may both find it free and spawn; the second relay then
// finds the lock taken and exits, which is cheaper than a lock handover.
//
// The relay is a detached child of the command rather than a launchd agent
// on purpose: macOS treats a launchd-started binary as its own process for
// the Local Network permission and silently denies it (every connection to
// a vmnet address fails with "no route to host"), while a child of the
// terminal inherits the terminal's grant, like the ssh ProxyCommand does. The
// relay therefore lives until logout and every command brings it back.
func (b *Backend) ensureRelay(ctx context.Context) error {
	targets, err := b.forwardTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	alive, err := b.relayAlive()
	if err != nil || alive {
		return err
	}
	if err := b.spawn(b.exe, b.paths); err != nil {
		return fmt.Errorf("start the forward relay: %w", err)
	}
	return nil
}

// relayAlive reports whether a relay holds the lock.
func (b *Backend) relayAlive() (bool, error) {
	f, err := os.OpenFile(b.paths.RelayLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", b.paths.RelayLockPath(), err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	}
	return false, fmt.Errorf("lock %s: %w", b.paths.RelayLockPath(), err)
}

// spawnRelay is the production Spawner: `skiff _relay` in its own session
// with the log as its only output, reaped by a goroutine so it never lingers
// as a zombie of a long-running command (tunnel).
func spawnRelay(exe string, paths config.Paths) error {
	log, err := os.OpenFile(paths.RelayLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", paths.RelayLogPath(), err)
	}
	defer log.Close()
	cmd := exec.Command(exe, "_relay")
	cmd.Env = append(os.Environ(), "SKIFF_HOME="+paths.Home)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// kickRelayFor starts the relay again for VMs about to run that have
// forwards (a relay from before logout is gone; one that idled out too).
func (b *Backend) kickRelayFor(ctx context.Context, names ...string) {
	for _, name := range names {
		rec, _, err := b.loadRecord(name)
		if err != nil || len(rec.Forwards) == 0 {
			continue
		}
		if err := b.ensureRelay(ctx); err != nil {
			b.statusf("  [WARN] %v; port forwards stay dead until 'skiff forward' is run again\n", err)
		}
		return
	}
}
