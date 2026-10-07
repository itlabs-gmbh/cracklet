package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"
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
	if err := os.MkdirAll(a.paths.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", a.paths.Home, err)
	}
	f, err := os.OpenFile(a.paths.LimaLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lima lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, busy
		}
		return nil, fmt.Errorf("lock lima: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
