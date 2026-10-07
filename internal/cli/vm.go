package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func newNewCmd(get func() *app.App) *cobra.Command {
	spec := vm.Spec{}
	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create and boot a microVM",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				spec.Name = args[0]
			}
			_, err := get().NewVM(cmd.Context(), spec)
			return err
		},
	}
	cmd.Flags().IntVar(&spec.VCPUs, "vcpus", config.DefaultVCPUs, "number of vCPUs")
	cmd.Flags().IntVar(&spec.MemMiB, "mem", config.DefaultMemMiB, "memory in MiB")
	cmd.Flags().StringVar(&spec.Disk, "disk", "", "root disk size, e.g. 4G (default: the profile's image size; anything else cold-boots)")
	cmd.Flags().StringVar(&spec.Profile, "profile", vm.ProfileBase,
		"guest image: "+strings.Join(vm.ProfileNames(), ", ")+" (build it first with 'cracklet prepare --profile')")
	cmd.Flags().StringArrayVarP(&spec.Forwards, "port", "p", nil, "forward localhost:[HOST:]GUEST to the VM (repeatable)")
	cmd.Flags().BoolVar(&spec.Fresh, "fresh", false, "cold-boot instead of restoring the golden snapshot")
	cmd.Flags().StringArrayVar(&spec.Grants, "grant", nil, "grant a capability right away, e.g. claude or github:org/repo (repeatable)")
	return cmd
}

func newForwardCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "forward NAME [[HOST:]GUEST...]",
		Short: "Expose VM ports on localhost (lists forwards when no ports are given)",
		Example: "  cracklet forward web 8080        # localhost:8080 -> web:8080\n" +
			"  cracklet forward web 3000:80     # localhost:3000 -> web:80",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := get().Forward(cmd.Context(), args[0], args[1:])
			if err != nil {
				return err
			}
			if len(args) == 1 {
				printForwards(cmd, info)
			}
			return nil
		},
	}
}

func newUnforwardCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "unforward NAME HOSTPORT...",
		Short: "Remove port forwards by host port",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ports := make([]int, 0, len(args)-1)
			for _, p := range args[1:] {
				port, err := strconv.Atoi(p)
				if err != nil {
					return fmt.Errorf("invalid port %q", p)
				}
				ports = append(ports, port)
			}
			_, err := get().Unforward(cmd.Context(), args[0], ports)
			return err
		},
	}
}

func printForwards(cmd *cobra.Command, info app.VMInfo) {
	if len(info.Forwards) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s has no port forwards\n", info.Name)
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LOCAL\tGUEST\tSTATE")
	for _, f := range info.Forwards {
		fmt.Fprintf(w, "localhost:%d\t%s:%d\t%s\n", f.Host, info.Name, f.Guest, f.State)
	}
	_ = w.Flush()
}

func forwardLabels(info app.VMInfo) string {
	if len(info.Forwards) == 0 {
		return "-"
	}
	labels := make([]string, 0, len(info.Forwards))
	for _, f := range info.Forwards {
		labels = append(labels, f.Label())
	}
	return strings.Join(labels, ",")
}

func newRmCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Stop and delete a microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return get().RemoveVM(cmd.Context(), args[0])
		},
	}
}

func newStartCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "start NAME",
		Short: "Boot a stopped microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := get().StartVM(cmd.Context(), args[0])
			return err
		},
	}
}

func newStopCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "stop NAME",
		Short: "Shut a microVM down, keeping its disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := get().StopVM(cmd.Context(), args[0])
			return err
		},
	}
}

func newLsCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List microVMs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			vms, err := get().ListVMs(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATE\tIP\tVCPUS\tMEM\tPORTS\tGRANTS\tSSH")
			for _, v := range vms {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%dM\t%s\t%s\tssh %s.%s\n",
					v.Name, v.State, v.IP, v.VCPUs, v.MemMiB, forwardLabels(v), grantLabels(v), v.Name, config.Instance)
			}
			return w.Flush()
		},
	}
}

func newSSHCmd(get func() *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh NAME [command...]",
		Short: "Open a shell in a microVM (or run a command)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			remote := args[1:]
			// Allow `cracklet ssh NAME -- cmd` as well as `cracklet ssh NAME cmd`.
			if len(remote) > 0 && remote[0] == "--" {
				remote = remote[1:]
			}
			return get().SSH(cmd.Context(), args[0], remote)
		},
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func newTunnelCmd(get func() *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "tunnel NAME",
		Short: "Serve a microVM's broker without an interactive session",
		Long: `tunnel holds the broker forward into NAME until it is interrupted or NAME
stops, reconnecting when the connection drops. Use it when the guest is
reached by something other than 'cracklet ssh', e.g. an editor or agent
runner with its own ssh session. 'cracklet ssh NAME' keeps working alongside.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return get().Tunnel(cmd.Context(), args[0])
		},
	}
}
