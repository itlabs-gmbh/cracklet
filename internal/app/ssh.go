package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"syscall"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/sshcfg"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// sshConnectionFailed is ssh's exit status for transport-level failures, as
// opposed to the exit status of the remote command.
const sshConnectionFailed = 255

// RemoteExitError reports that the remote command finished with a non-zero
// status or that ssh died from a signal; ssh already showed its output, so the
// CLI only mirrors the code (and prints Reason when there is one).
type RemoteExitError struct {
	Code   int
	Reason string
}

func (e *RemoteExitError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

// SSH opens an interactive session (or runs command) on a microVM.
func (a *App) SSH(ctx context.Context, name string, command []string) error {
	if err := vm.ValidateName(name); err != nil {
		return err
	}
	if err := a.requireRunning(ctx); err != nil {
		return err
	}
	if err := a.ensureSSHConfig(); err != nil {
		return err
	}
	extra, stop, err := a.sshExtraArgs(ctx, name)
	if err != nil {
		return err
	}
	defer stop()
	err = a.r.Run(ctx, "ssh", append(extra, a.sshArgs(name, command)...)...)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	code := exitErr.ExitCode()
	switch {
	case code < 0:
		sig := signalOf(exitErr)
		return &RemoteExitError{Code: 128 + int(sig), Reason: fmt.Sprintf("ssh terminated by signal %s", sig)}
	case code == sshConnectionFailed:
		return fmt.Errorf("could not connect to %s (ssh exit 255); check that it is running with 'cracklet ls' (%w)", name, err)
	default:
		return &RemoteExitError{Code: code}
	}
}

// sshArgs always terminates option parsing with "--" so that a remote command
// can never be mistaken for a local ssh option such as -oProxyCommand.
func (a *App) sshArgs(name string, command []string) []string {
	args := []string{"-F", a.paths.SSHConfigPath(), sshcfg.HostAlias(name, config.Instance)}
	if len(command) > 0 {
		args = append(args, "--")
		args = append(args, command...)
	}
	return args
}

func signalOf(err *exec.ExitError) syscall.Signal {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal()
	}
	return syscall.SIGKILL
}

// ensureSSHConfig writes ~/.cracklet/ssh_config if it is missing.
func (a *App) ensureSSHConfig() error {
	_, err := os.Stat(a.paths.SSHConfigPath())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return a.writeSSHConfig()
	default:
		return fmt.Errorf("check ssh config: %w", err)
	}
}

func (a *App) writeSSHConfig() error {
	text, err := sshcfg.Render(sshcfg.Options{
		Instance:      config.Instance,
		KeyPath:       a.paths.KeyPath(),
		LimaSSHConfig: a.paths.LimaSSHConfig(config.Instance),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.paths.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", a.paths.Home, err)
	}
	if err := os.WriteFile(a.paths.SSHConfigPath(), []byte(text), 0o600); err != nil {
		return fmt.Errorf("write ssh config: %w", err)
	}
	return nil
}
