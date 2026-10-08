package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Starting a microVM and restarting the Lima VM for a resize exclude each
// other through a host-side flock: starts share it, a resize holds it
// exclusively from its running-microVM check until the Lima VM is back. The
// agent's own lock cannot do this, since `limactl stop` runs outside of it.
// Both sides fail fast instead of waiting, because a blocked flock would not
// notice Ctrl-C.
var (
	errResizing    = errors.New("the Lima VM is being resized by 'cracklet prepare'; retry when it has finished")
	errStartingVMs = errors.New("another cracklet command is creating or starting a microVM; retry the resize when it has finished")
)

// holdLimaForMicroVM keeps a resize from restarting the Lima VM under a starting microVM.
func (a *App) holdLimaForMicroVM() (func(), error) {
	return a.lockLima(syscall.LOCK_SH, errResizing)
}

// holdLimaForResize keeps microVMs from starting while the Lima VM is resized.
func (a *App) holdLimaForResize() (func(), error) {
	return a.lockLima(syscall.LOCK_EX, errStartingVMs)
}

func (a *App) lockLima(how int, busy error) (func(), error) {
	f, err := a.openLock(a.paths.LimaLockPath())
	if err != nil {
		return nil, err
	}
	release, err := tryFlock(f, how)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		_ = f.Close()
		return nil, busy
	}
	return release, err
}

// bootLockPoll is how often waitForLimaBoot retries a held boot lock.
const bootLockPoll = 100 * time.Millisecond

// waitForLimaBoot serializes starting the stopped Lima VM, so commands run in
// parallel after a reboot of the Mac (a tool starting its pool of microVMs)
// boot it once. Unlike the Lima lock it waits, polling so Ctrl-C still works.
func (a *App) waitForLimaBoot(ctx context.Context) (func(), error) {
	f, err := a.openLock(a.paths.LimaBootLockPath())
	if err != nil {
		return nil, err
	}
	for {
		release, err := tryFlock(f, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return release, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(bootLockPoll):
		}
	}
}

func (a *App) openLock(path string) (*os.File, error) {
	if err := os.MkdirAll(a.paths.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", a.paths.Home, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// tryFlock takes the lock without blocking. It closes f on any error but
// EWOULDBLOCK, which it returns unwrapped so the caller may retry.
func tryFlock(f *os.File, how int) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, err
		}
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
