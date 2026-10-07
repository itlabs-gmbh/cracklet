package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/itlabs-gmbh/cracklet/internal/app"
)

func newCapCmd(get func() *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cap",
		Short: "Manage capabilities (what the broker can hand into a microVM)",
		Long: `Capabilities are TOML files. cracklet ships a few (claude, github); your own
live in ~/.cracklet/caps/<name>.toml and override embedded ones of the same name.
A capability says how something is handed into the guest; 'cracklet grant' says whether.`,
	}
	var render bool
	var vmName string
	show := &cobra.Command{
		Use:   "show NAME",
		Short: "Print a capability file, or what a guest would receive with --render",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return get().CapShow(args[0], render, vmName)
		},
	}
	show.Flags().BoolVar(&render, "render", false, "show the rendered guest configuration")
	show.Flags().StringVar(&vmName, "vm", "", "render for this VM (default: an example VM)")

	lint := &cobra.Command{
		Use:   "lint [NAME]",
		Short: "Validate your capability files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return get().CapLint(name)
		},
	}
	var yes bool
	add := &cobra.Command{
		Use:   "add URL",
		Short: "Download a capability file into ~/.cracklet/caps after showing it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return get().CapAdd(cmd.Context(), args[0], yes, cmd.InOrStdin())
		},
	}
	add.Flags().BoolVarP(&yes, "yes", "y", false, "install without asking")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "ls",
			Short: "List capabilities",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return get().CapList() },
		},
		show,
		lint,
		&cobra.Command{
			Use:   "init NAME",
			Short: "Write a commented skeleton for a new capability",
			Args:  cobra.ExactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return get().CapInit(args[0]) },
		},
		add,
	)
	return cmd
}

func newSecretCmd(get func() *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Store credentials in the macOS Keychain for capabilities to use",
		Long: `Secrets live in the macOS Keychain under the service "cracklet" and are
referenced from capability files as keychain:cracklet/<name>. The value is read
from the terminal without echo, or from stdin when it is not a terminal.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:     "set NAME",
			Short:   "Store a secret (prompts for the value)",
			Example: "  cracklet secret set claude-token\n  echo \"$TOKEN\" | cracklet secret set github-token",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				value, err := readSecret(cmd, args[0])
				if err != nil {
					return err
				}
				return get().SecretSet(cmd.Context(), args[0], value)
			},
		},
		&cobra.Command{
			Use:   "rm NAME",
			Short: "Delete a stored secret",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return get().SecretRemove(cmd.Context(), args[0])
			},
		},
	)
	return cmd
}

// readSecret reads the value without echo from a terminal, or the first
// line of stdin otherwise.
func readSecret(cmd *cobra.Command, name string) (string, error) {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(cmd.ErrOrStderr(), "value for %s: ", name)
		raw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read secret from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}
