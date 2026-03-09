package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/auth"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure agent tools to use Telara MCP",
}

var setupConfigName string
var setupGlobal bool

var setupClaudeCodeCmd = &cobra.Command{
	Use:   "claude-code",
	Short: "Configure Claude Code to use Telara MCP",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)

		var selectedConfig *api.MCPConfig
		if setupConfigName != "" {
			detail, err := client.GetConfig(context.Background(), setupConfigName)
			if err != nil {
				return fmt.Errorf("config %q not found: %w", setupConfigName, err)
			}
			selectedConfig = &detail.MCPConfig
		} else {
			resp, err := client.ListConfigs(context.Background())
			if err != nil {
				return fmt.Errorf("failed to list configs: %w", err)
			}
			if len(resp.Configs) == 0 {
				return fmt.Errorf("no MCP configurations available — create one at https://app.telara.ai")
			}
			options := make([]string, len(resp.Configs))
			for i, c := range resp.Configs {
				options[i] = c.Name
			}
			var chosen string
			prompt := &survey.Select{
				Message: "Select MCP configuration:",
				Options: options,
			}
			if err := survey.AskOne(prompt, &chosen); err != nil {
				return fmt.Errorf("selection cancelled: %w", err)
			}
			for i, name := range options {
				if name == chosen {
					selectedConfig = &resp.Configs[i]
					break
				}
			}
		}

		// Generate an API key for this configuration
		keyResp, err := client.GenerateKey(context.Background(), selectedConfig.ID, api.GenerateKeyRequest{
			Name: "telara-cli",
		})
		if err != nil {
			return fmt.Errorf("failed to generate API key: %w", err)
		}

		// Determine ~/.claude/settings.json path
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		claudeDir := filepath.Join(homeDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0700); err != nil {
			return fmt.Errorf("failed to create .claude directory: %w", err)
		}
		settingsPath := filepath.Join(claudeDir, "settings.json")

		// Read existing settings if present
		var settings map[string]interface{}
		if data, err := os.ReadFile(settingsPath); err == nil {
			if err := json.Unmarshal(data, &settings); err != nil {
				settings = make(map[string]interface{})
			}
		} else {
			settings = make(map[string]interface{})
		}

		// Merge Telara entry into mcpServers
		mcpServers, _ := settings["mcpServers"].(map[string]interface{})
		if mcpServers == nil {
			mcpServers = make(map[string]interface{})
		}
		mcpURL := keyResp.MCPURL
		if mcpURL == "" {
			mcpURL = "https://mcp.telara.ai/sse"
		}
		mcpServers["telara"] = map[string]interface{}{
			"type": "sse",
			"url":  mcpURL,
			"headers": map[string]string{
				"Authorization": "Bearer " + keyResp.RawKey,
			},
		}
		settings["mcpServers"] = mcpServers

		// Write atomically via temp file + rename
		tmpPath := settingsPath + ".tmp"
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal settings: %w", err)
		}
		if err := os.WriteFile(tmpPath, data, 0600); err != nil {
			return fmt.Errorf("failed to write settings: %w", err)
		}
		if err := os.Rename(tmpPath, settingsPath); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}

		fmt.Fprintf(os.Stdout, "Updated %s\n", settingsPath)
		fmt.Fprintf(os.Stdout, "Config:  %s\n", selectedConfig.Name)
		fmt.Fprintf(os.Stdout, "MCP URL: %s\n", mcpURL)
		fmt.Fprintln(os.Stdout, "\nRestart Claude Code to connect.")
		return nil
	},
}

func init() {
	setupClaudeCodeCmd.Flags().StringVar(&setupConfigName, "config", "", "MCP configuration name or ID")
	setupClaudeCodeCmd.Flags().BoolVar(&setupGlobal, "global", true, "Write to global config (~/.claude/settings.json)")
	setupCmd.AddCommand(setupClaudeCodeCmd)
	rootCmd.AddCommand(setupCmd)
}
