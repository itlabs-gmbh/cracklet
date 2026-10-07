// Package cli defines the cracklet command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// appBuilder constructs the App once the output writer is known.
type appBuilder func(out io.Writer) (*app.App, error)

func defaultBuilder(out io.Writer) (*app.App, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return app.New(runner.NewExec(), paths, out), nil
}

// Main runs the CLI and returns the process exit code. Ctrl-C or SIGTERM
// cancels the context so running limactl/ssh children are interrupted too.
func Main() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return exitCode(newRoot(defaultBuilder).ExecuteContext(ctx), os.Stderr)
}

// exitCode maps an error to a process exit status. Remote command failures
// from `cracklet ssh` keep their status and stay silent, like plain ssh.
func exitCode(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var remote *app.RemoteExitError
	if errors.As(err, &remote) {
		if remote.Reason != "" {
			fmt.Fprintln(stderr, "cracklet:", remote.Reason)
		}
		return remote.Code
	}
	fmt.Fprintln(stderr, "cracklet:", err)
	return 1
}

func newRoot(build appBuilder) *cobra.Command {
	var a *app.App
	root := &cobra.Command{
		Use:   "cracklet",
		Short: "Firecracker microVMs on your Mac",
		Long: `cracklet runs real Firecracker microVMs on Apple Silicon. A single Lima VM with
nested virtualization hosts Firecracker; every microVM gets its own kernel,
root disk, IP address and SSH access.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) (err error) {
			out := cmd.OutOrStdout()
			if wantsJSON(cmd) {
				out = cmd.ErrOrStderr()
			}
			a, err = build(out)
			return err
		},
	}
	get := func() *app.App { return a }
	root.AddCommand(
		newPrepareCmd(get),
		newDoctorCmd(get),
		newNewCmd(get),
		newRmCmd(get),
		newLsCmd(get),
		newInspectCmd(get),
		newSSHCmd(get),
		newExecCmd(get),
		newStartCmd(get),
		newStopCmd(get),
		newForwardCmd(get),
		newUnforwardCmd(get),
		newGrantCmd(get),
		newRevokeCmd(get),
		newGrantsCmd(get),
		newCapCmd(get),
		newSecretCmd(get),
	)
	return root
}
