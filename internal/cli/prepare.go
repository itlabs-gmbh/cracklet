package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func newPrepareCmd(get func() *app.App) *cobra.Command {
	var o app.PrepareOptions
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Set up Lima, Firecracker, kernel and base image (idempotent)",
		Long: "Set up Lima, Firecracker, kernel and base image (idempotent).\n\n" +
			"--cpus, --memory and --disk size a new Lima VM. Given explicitly on an existing\n" +
			"one, they resize it in place: the Lima VM restarts, so stop running microVMs first.\n" +
			"The disk can only grow.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			opts := o
			opts.Explicit = app.SizeFlags{CPUs: f.Changed("cpus"), Memory: f.Changed("memory"), Disk: f.Changed("disk")}
			return get().Prepare(cmd.Context(), opts)
		},
	}
	cmd.Flags().IntVar(&o.CPUs, "cpus", config.DefaultLimaCPUs, "CPUs for the Lima VM")
	cmd.Flags().IntVar(&o.MemoryGiB, "memory", config.DefaultLimaMemoryGiB, "memory for the Lima VM in GiB")
	cmd.Flags().IntVar(&o.DiskGiB, "disk", config.DefaultLimaDiskGiB, "disk for the Lima VM in GiB (grow only)")
	cmd.Flags().StringArrayVar(&o.Profiles, "profile", nil,
		"also build this guest image profile and its golden snapshot (repeatable): "+strings.Join(vm.ProfileNames(), ", "))
	return cmd
}

func newDoctorCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this Mac can run cracklet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return get().Doctor(cmd.Context())
		},
	}
}
