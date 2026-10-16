package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/skiff/internal/app"
	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/vm"
)

func newPrepareCmd(get func() *app.App) *cobra.Command {
	var o app.PrepareOptions
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Set up the backend runtime and the guest images (idempotent)",
		Long: "Set up the backend runtime and the guest images (idempotent).\n\n" +
			"--backend picks how VMs run and is remembered in ~/.skiff/backend:\n" +
			"  container  one Virtualization.framework VM per VM via Apple's container (default)\n" +
			"  lima       Firecracker inside a Lima VM with nested virtualization (deprecated)\n\n" +
			"--cpus, --memory and --disk size a new Lima VM. Given explicitly on an existing\n" +
			"one, they resize it in place: the Lima VM restarts, so stop running VMs first.\n" +
			"The disk can only grow. The container backend has no shared VM to size.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			opts := o
			opts.Explicit = app.SizeFlags{CPUs: f.Changed("cpus"), Memory: f.Changed("memory"), Disk: f.Changed("disk")}
			if chosenBackend(cmd) != "" {
				if err := get().CheckBackendSwitch(cmd.Context()); err != nil {
					return err
				}
			}
			return get().Prepare(cmd.Context(), opts)
		},
	}
	cmd.Flags().String(backendFlag, "", "backend to use from now on: "+strings.Join(backend.Names(), " or "))
	cmd.Flags().IntVar(&o.CPUs, "cpus", config.DefaultLimaCPUs, "CPUs for the Lima VM")
	cmd.Flags().IntVar(&o.MemoryGiB, "memory", config.DefaultLimaMemoryGiB, "memory for the Lima VM in GiB")
	cmd.Flags().IntVar(&o.DiskGiB, "disk", config.DefaultLimaDiskGiB, "disk for the Lima VM in GiB (grow only)")
	cmd.Flags().StringArrayVar(&o.Profiles, "profile", nil,
		"also build this guest image profile and its golden snapshot (repeatable): "+strings.Join(vm.ProfileNames(), ", "))
	return cmd
}

// backendFlag is read before the App is built, so the chosen backend runs prepare.
const backendFlag = "backend"

// chosenBackend returns a validated --backend value, or "" when absent.
func chosenBackend(cmd *cobra.Command) string {
	f := cmd.Flags().Lookup(backendFlag)
	if f == nil || !f.Changed {
		return ""
	}
	return f.Value.String()
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
