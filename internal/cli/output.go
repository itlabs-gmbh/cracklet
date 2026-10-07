package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

// jsonFlag is the name of the --json flag shared by the commands that report VMs.
const jsonFlag = "json"

func addJSONFlag(cmd *cobra.Command, target *bool) {
	cmd.Flags().BoolVar(target, jsonFlag, false, "print the result as JSON (progress messages go to stderr)")
}

// wantsJSON reports whether --json was given. The root command uses it to send
// the App's progress messages to stderr so stdout carries only the JSON.
func wantsJSON(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup(jsonFlag)
	return f != nil && f.Value.String() == "true"
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
