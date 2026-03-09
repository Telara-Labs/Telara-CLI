package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/display"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage MCP configurations",
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List accessible MCP configurations",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)
		resp, err := client.ListConfigs(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list configs: %w", err)
		}
		if len(resp.Configs) == 0 {
			fmt.Fprintln(os.Stdout, "No MCP configurations found.")
			return nil
		}
		t := &display.Table{Headers: []string{"NAME", "SCOPE", "DATA SOURCES", "STATUS"}}
		for _, c := range resp.Configs {
			scope := c.ScopeType
			if c.ScopeID != "" {
				scope = c.ScopeType + "/" + c.ScopeID
			}
			t.AddRow(c.Name, scope, fmt.Sprintf("%d", c.DataSources), c.Status)
		}
		t.Print(os.Stdout)
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show <name-or-id>",
	Short: "Show details of an MCP configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)
		cfg, err := client.GetConfig(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}
		fmt.Fprintf(os.Stdout, "Name:         %s\n", cfg.Name)
		fmt.Fprintf(os.Stdout, "ID:           %s\n", cfg.ID)
		fmt.Fprintf(os.Stdout, "Scope:        %s\n", cfg.ScopeType)
		fmt.Fprintf(os.Stdout, "Status:       %s\n", cfg.Status)
		fmt.Fprintf(os.Stdout, "MCP URL:      %s\n", cfg.MCPURL)
		fmt.Fprintf(os.Stdout, "Policies:     %d\n", cfg.PolicyCount)
		fmt.Fprintf(os.Stdout, "API Keys:     %d\n", cfg.KeyCount)
		if len(cfg.DataSources) > 0 {
			names := make([]string, len(cfg.DataSources))
			for i, ds := range cfg.DataSources {
				names[i] = ds.Name + " (" + ds.Integration + ")"
			}
			fmt.Fprintf(os.Stdout, "Data Sources: %s\n", strings.Join(names, ", "))
		} else {
			fmt.Fprintf(os.Stdout, "Data Sources: none\n")
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configShowCmd)
	rootCmd.AddCommand(configCmd)
}
