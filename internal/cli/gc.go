package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/itlabs-gmbh/cracklet/internal/app"
)

func newGCCmd(get func() *app.App) *cobra.Command {
	var opts app.GCOptions
	var olderThan string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove orphaned microVMs and leftover host state",
		Long: `gc removes microVMs that a tool created with 'cracklet new --owner' and no
longer tracks. Only VMs with an owner are ever collected, so VMs made by hand
are safe. --owner and --older-than select which owned VMs go; without either,
gc removes no VM and only deletes the grants and tokens on the Mac of VMs that
no longer exist.

A tool that keeps a pool of VMs reconciles with
  cracklet gc --owner NAME --keep VM --keep VM ...
listing every VM it still tracks. VMs it is creating right now are not in its
list yet; protect them with --keep or --older-than. VMs that 'cracklet new' is
still building are never collected.

If removing one VM fails, gc carries on with the others and exits non-zero;
--json then still prints what was removed.`,
		Example: "  cracklet gc --owner paseo --dry-run\n" +
			"  cracklet gc --older-than 7d\n" +
			"  cracklet gc --owner paseo --keep paseo-1 --keep paseo-2",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			age, err := parseAge(olderThan)
			if err != nil {
				return err
			}
			opts.OlderThan = age
			res, err := get().GC(cmd.Context(), opts)
			// res.VMs is nil only when gc failed before looking at any VM;
			// otherwise partial results are printed and the error follows.
			if asJSON && res.VMs != nil {
				if jerr := writeJSON(cmd, res); jerr != nil && err == nil {
					return jerr
				}
			}
			return err
		},
	}
	addJSONFlag(cmd, &asJSON)
	cmd.Flags().StringVar(&opts.Owner, "owner", "", "collect only the VMs of this owner")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "collect only VMs created at least this long ago, e.g. 90m, 24h or 7d")
	cmd.Flags().StringArrayVar(&opts.Keep, "keep", nil, "never collect this VM (repeatable)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show what would be removed without removing it")
	return cmd
}

var daysRe = regexp.MustCompile(`^(\d+)d$`)

// parseAge accepts Go durations ("90m", "24h") plus whole days ("7d"),
// which time.ParseDuration lacks; empty means no age limit.
func parseAge(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if m := daysRe.FindStringSubmatch(s); m != nil {
		days, err := strconv.Atoi(m[1])
		if err != nil || days > int(time.Duration(1<<63-1)/(24*time.Hour)) {
			return 0, fmt.Errorf("invalid age %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid age %q (use e.g. 90m, 24h or 7d)", s)
	}
	return d, nil
}
