package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func newPrepareCmd(get func() *app.App) *cobra.Command {
	var o app.PrepareOptions
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Set up the backend runtime and the guest images (idempotent)",
		Long: "Set up the backend runtime and the guest images (idempotent).\n\n" +
			"--backend picks how microVMs run and is remembered in ~/.cracklet/backend:\n" +
			"  lima       Firecracker inside a Lima VM with nested virtualization (default)\n" +
			"  container  one Virtualization.framework VM per microVM via Apple's container\n\n" +
			"--cpus, --memory and --disk size a new Lima VM. Given explicitly on an existing\n" +
			"one, they resize it in place: the Lima VM restarts, so stop running microVMs first.\n" +
			"The disk can only grow. The container backend has no shared VM to size.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			opts := o
			opts.Explicit = app.SizeFlags{CPUs: f.Changed("cpus"), Memory: f.Changed("memory"), Disk: f.Changed("disk")}
			if err := get().Prepare(cmd.Context(), opts); err != nil {
				return err
			}
			// Only a prepared backend becomes the stored choice; a failed
			// prepare must not strand every later command on it.
			if chosenBackend(cmd) != "" {
				return get().RememberBackend()
			}
			return nil
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
		Short: "Check whether this Mac can run cracklet",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return get().Doctor(cmd.Context())
		},
	}
}
