package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
)

func newGrantCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "grant NAME CAP...",
		Short: "Let a microVM use capabilities through the broker",
		Long: `grant allows a microVM to use capabilities. The guest is configured right
away; the broker itself runs on the Mac for as long as 'cracklet ssh NAME' is open
and hands out connections, never secrets. Default is deny.

A grant covers everything the capability's credential can reach; limit the
credential itself, such as a fine-grained GitHub token for the repositories
the agent should work on.`,
		Example: "  cracklet grant agent1 claude\n" +
			"  cracklet grant agent1 github ssh-agent",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := get().Grant(cmd.Context(), args[0], args[1:])
			return err
		},
	}
}

func newRevokeCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke NAME CAP...",
		Short: "Withdraw capabilities from a microVM",
		Long: `revoke withdraws grants and removes their guest configuration. Nothing is
withdrawn when one of the capabilities is not granted.`,
		Example: "  cracklet revoke agent1 github\n" +
			"  cracklet revoke agent1 claude ssh-agent",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := get().Revoke(cmd.Context(), args[0], args[1:])
			return err
		},
	}
}

func newGrantsCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "grants NAME",
		Short: "Show what a microVM may use",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := get().Grants(args[0])
			if err != nil {
				return err
			}
			if len(set) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s has no grants\n", args[0])
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.Join(set.Caps(), "\n"))
			return nil
		},
	}
}

func grantLabels(info app.VMInfo) string {
	if len(info.Grants) == 0 {
		return "-"
	}
	return strings.Join(info.Grants, ",")
}
