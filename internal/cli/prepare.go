package cli

import (
	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/skiff/internal/app"
)

func newPrepareCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "prepare",
		Short: "Set up Apple's container runtime and the guest image (idempotent)",
		Long: `Set up Apple's container runtime and the guest image (idempotent): installs the
container CLI through Homebrew if needed, starts its service, generates the SSH
key pair, builds the guest image and writes ~/.skiff/ssh_config.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return get().Prepare(cmd.Context())
		},
	}
}

func newDoctorCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this Mac can run skiff",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return get().Doctor(cmd.Context())
		},
	}
}
