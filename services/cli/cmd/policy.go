package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

// telara policy — which of your own policies governs your sessions.
//
// Your sessions run on the Default (everything you can reach, within any limit
// an admin set) until you pick one of your own narrower policies. The pick is
// account-wide: every MCP client, Aniya and the Slack fallback use it. It can
// only narrow; an admin's limits always apply on top.

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Choose which of your own policies governs your sessions",
	Long: `Your sessions run on the Default — everything you can reach, within any limit
an admin set — until you pick one of your own narrower policies. The pick applies
to every AI tool, Aniya and Slack. It can only narrow what you can do.

  telara policy list              your policies; * marks the active one
  telara policy use <name-or-id>  make one active
  telara policy use --default     go back to the Default`,
}

var policyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your own policies and which one is active",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAuthenticatedClient()
		if err != nil {
			return err
		}
		resp, err := client.ListMyPolicies(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list your policies: %w", err)
		}
		if resp.ActivePolicyID == "" {
			display.PrintInfo("Active: Default (everything you can reach, within your admin's limits)")
		}
		if len(resp.Policies) == 0 {
			fmt.Fprintln(os.Stdout, "You have no policies of your own. Create one in the dashboard under Policies.")
			return nil
		}
		t := &display.Table{Headers: []string{"", "NAME", "ID", "DESCRIPTION"}}
		for _, p := range resp.Policies {
			mark := ""
			if p.Active {
				mark = "*"
			}
			t.AddRow(mark, p.Name, p.ID, p.Description)
		}
		t.Print(os.Stdout)
		return nil
	},
}

var policyUseDefault bool

var policyUseCmd = &cobra.Command{
	Use:   "use [name-or-id] | --default",
	Short: "Make one of your policies active, or go back to the Default",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := strings.TrimSpace(strings.Join(args, " "))
		if policyUseDefault == (ref != "") {
			return fmt.Errorf("name a policy, or pass --default (telara policy list)")
		}
		client, err := newAuthenticatedClient()
		if err != nil {
			return err
		}
		if policyUseDefault {
			if err := client.UseDefaultPolicy(context.Background()); err != nil {
				return fmt.Errorf("failed to go back to the Default: %w", err)
			}
			display.PrintSuccess("Active: Default. Open sessions pick this up within a minute.")
			return nil
		}
		name, err := client.UseMyPolicy(context.Background(), ref)
		if err != nil {
			return fmt.Errorf("failed to use %q: %w", ref, err)
		}
		display.PrintSuccess(fmt.Sprintf("Active: %s. Open sessions pick this up within a minute.", name))
		return nil
	},
}

func init() {
	policyUseCmd.Flags().BoolVar(&policyUseDefault, "default", false, "go back to the Default")
	policyCmd.AddCommand(policyListCmd)
	policyCmd.AddCommand(policyUseCmd)
	rootCmd.AddCommand(policyCmd)
}
