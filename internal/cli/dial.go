package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
)

// annotationStdoutIsData marks commands whose stdout carries a byte stream,
// so the App's own notes must go to stderr instead.
const annotationStdoutIsData = "stdout-is-data"

func newDialCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:    "_dial HOST PORT",
		Short:  "ssh ProxyCommand: relay stdin/stdout to a port on a microVM",
		Hidden: true,
		Long: `_dial is the ProxyCommand in ~/.cracklet/ssh_config for backends whose guests
the Mac reaches directly. ssh runs it as "cracklet _dial %h %p"; HOST is the
alias <vm>.cracklet or a bare VM name.`,
		Args:        cobra.ExactArgs(2),
		Annotations: map[string]string{annotationStdoutIsData: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port %q", args[1])
			}
			return get().Dial(cmd.Context(), args[0], port, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

func newRelayCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:    "_relay",
		Short:  "Serve the port forwards of the container backend (started by cracklet itself)",
		Hidden: true,
		Long: `_relay listens on 127.0.0.1:HOST for every forward of every microVM and joins
each connection to the VM's guest port. cracklet starts it as a detached process
whenever forwards exist and none runs (log: ~/.cracklet/relay.log); it exits on
its own once no forwards are left.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return get().Relay(cmd.Context())
		},
	}
}
