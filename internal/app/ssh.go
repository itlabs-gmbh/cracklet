package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"syscall"

	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/shquote"
	"github.com/itlabs-gmbh/skiff/internal/sshcfg"
	"github.com/itlabs-gmbh/skiff/internal/vm"
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

// SSH opens an interactive session (or runs command) on a microVM. Like plain
// ssh, the words of command are joined with spaces and interpreted by the
// guest shell, so pipes and quoting in them are shell syntax.
func (a *App) SSH(ctx context.Context, name string, command []string) error {
	if err := vm.ValidateName(name); err != nil {
		return err
	}
	return a.session(ctx, name, command)
}

// Exec runs argv on a microVM exactly as given: every word is quoted, so the
// guest shell sees the same argument vector instead of re-splitting it.
func (a *App) Exec(ctx context.Context, name string, argv []string) error {
	if err := vm.ValidateName(name); err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("exec needs a command to run, e.g. 'skiff exec NAME -- uname -a'")
	}
	return a.session(ctx, name, []string{shquote.Join(argv)})
}

// session runs ssh with the broker tunnel attached and maps its exit status.
func (a *App) session(ctx context.Context, name string, command []string) error {
	if err := a.backend.Ready(ctx); err != nil {
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
		hint := "check that it is running with 'skiff ls'"
		if a.backend.KnownHostsFile() != "" {
			hint += "; if ssh reported a host key problem, the VM predates key pinning or was replaced: recreate it"
		}
		return fmt.Errorf("could not connect to %s (ssh exit 255); %s (%w)", name, hint, err)
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

// ensureSSHConfig writes ~/.skiff/ssh_config when it is missing or no
// longer what the backend renders (an upgrade that pins host keys, a switch
// of backends), so a session never runs with the settings of an older
// version.
func (a *App) ensureSSHConfig() error {
	text, err := a.renderSSHConfig()
	if err != nil {
		return err
	}
	have, err := os.ReadFile(a.paths.SSHConfigPath())
	switch {
	case err == nil && string(have) == text:
		return nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("check ssh config: %w", err)
	}
	return a.writeSSHConfig()
}

func (a *App) renderSSHConfig() (string, error) {
	return sshcfg.Render(sshcfg.Options{
		Instance:       config.Instance,
		KeyPath:        a.paths.KeyPath(),
		ProxyCommand:   a.backend.SSHProxyCommand(),
		KnownHostsFile: a.backend.KnownHostsFile(),
	})
}

func (a *App) writeSSHConfig() error {
	text, err := a.renderSSHConfig()
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
