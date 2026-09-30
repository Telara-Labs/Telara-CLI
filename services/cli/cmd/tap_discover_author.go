package cmd

// tap_discover_author.go is the author path under `telara tap discover`
// (TENG-2936): brief, validate and save, exactly as `tap discover` runs them.
// Their flags belong to the discover module, so the arguments are passed
// through whole rather than parsed twice.

import (
	"fmt"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/tap-runtime/discover"
)

func init() {
	for _, c := range []struct{ name, short string }{
		{"brief", "Write a private authoring brief for a selected task or a discover candidate"},
		{"validate", "Run an authored package through the tap runner against a frozen case file and an oracle"},
		{"save", "Save an authored package, marked validated only for the digest its receipts passed"},
	} {
		name := c.name
		tapDiscoverCmd.AddCommand(&cobra.Command{
			Use:                name,
			Short:              c.short,
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				code := discover.Command(append([]string{name}, args...), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				if code != 0 {
					cmd.SilenceUsage = true
					return fmt.Errorf("tap discover %s exited %d", name, code)
				}
				return nil
			},
		})
	}
}
