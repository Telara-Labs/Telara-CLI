package cmd

// tap_discover.go is `telara tap discover`: the TAP runner's own `tap
// discover`, run with the same arguments. The runner ships the discovery
// engine and is updated on its own, so the CLI never carries an older copy.
// Discovery reads local files and sends nothing; publishing a saved
// primitive to Telara is `telara tap publish`.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
)

// runnerInstallHint is how to get the runner when it is not on PATH.
const runnerInstallHint = "the tap runner is not installed on this machine; install it with: npm install -g @telaralabs/tap (https://github.com/Telara-Labs/TAP-Runtime)"

var tapDiscoverCmd = &cobra.Command{
	Use:   "discover [flags]",
	Short: "Find recurring work in this machine's agent session history",
	Long: `Runs the TAP runner's own discover: it reads the session history the
agents on this machine keep, finds the work you repeat, and lets you save
each repeated task as a primitive. Every flag and subcommand is the runner's
(see: tap discover --help).

Everything happens locally. Nothing is uploaded. To share a saved primitive
with your Telara tenant, run: telara tap publish <saved-folder>`,
	DisableFlagParsing: true,
	RunE:               runTapDiscover,
}

func init() {
	tapCmd.AddCommand(tapDiscoverCmd)
}

func runTapDiscover(cmd *cobra.Command, args []string) error {
	runner := findRunner()
	if runner == "" {
		cmd.SilenceUsage = true
		return errors.New(runnerInstallHint)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	c := exec.CommandContext(ctx, runner, append([]string{"discover"}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		cmd.SilenceUsage = true
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("tap discover exited %d", exit.ExitCode())
		}
		return err
	}
	return nil
}
