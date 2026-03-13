package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/agent"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
)

// toolKeyName returns a human-friendly API key name for a given tool,
// incorporating the machine hostname for easy identification.
func toolKeyName(toolName string) string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return toolName
	}
	return toolName + "-" + hostname
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Connect an agentic coding tool to your Telara integrations and knowledge",
	Long: `Writes the Telara MCP server entry into your AI tool's global config file,
giving it access to the integrations, tools, and knowledge defined in the
selected configuration. Run without a subcommand for interactive setup.`,
	// Interactive mode when no subcommand is provided.
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInteractiveSetup()
	},
}

var setupConfigName string
var setupManaged bool

// selectDeployment fetches the accessible deployments for the given config and either
// returns the only one silently, errors if there are none, or prompts the user to pick.
func selectDeployment(client *api.Client, configID string) (*api.Deployment, error) {
	resp, err := client.ListDeployments(context.Background(), configID)
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}
	switch len(resp.Deployments) {
	case 0:
		return nil, fmt.Errorf("no deployments available for this configuration — ask your admin to deploy it to your scope")
	case 1:
		return &resp.Deployments[0], nil
	}

	options := make([]string, len(resp.Deployments))
	for i, d := range resp.Deployments {
		options[i] = d.ScopeName
	}
	var chosen string
	prompt := &survey.Select{
		Message: "Select deployment scope:",
		Options: options,
	}
	if err := survey.AskOne(prompt, &chosen); err != nil {
		return nil, fmt.Errorf("selection cancelled: %w", err)
	}
	for i, name := range options {
		if name == chosen {
			return &resp.Deployments[i], nil
		}
	}
	return nil, fmt.Errorf("deployment selection not found")
}

// runSetupForWriter runs the full setup flow for a single AgentWriter using the
// given scope. It prompts for config selection if setupConfigName is empty.
func runSetupForWriter(w agent.AgentWriter, scope agent.Scope) error {
	token, err := auth.LoadToken(prefs.APIURL)
	if err != nil {
		return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
	}
	client := api.NewClient(prefs.APIURL, token)

	selectedConfig, err := selectConfig(client)
	if err != nil {
		return err
	}

	deployment, err := selectDeployment(client, selectedConfig.ID)
	if err != nil {
		return err
	}

	keyResp, err := client.GenerateKey(context.Background(), selectedConfig.ID, api.GenerateKeyRequest{
		Name:      toolKeyName(w.Name()),
		ScopeType: deployment.ScopeType,
		ScopeID:   deployment.ScopeID,
	})
	if err != nil {
		return fmt.Errorf("failed to generate API key: %w", err)
	}

	mcpURL := keyResp.MCPURL
	if mcpURL == "" {
		mcpURL = prefs.APIURL + "/v1/mcp/sse"
	}

	entry := agent.MCPEntry{
		Type: "sse",
		URL:  mcpURL,
		Headers: map[string]string{
			"Authorization": "Bearer " + keyResp.RawKey,
		},
	}

	if err := w.Write(scope, "telara", entry); err != nil {
		return fmt.Errorf("failed to write %s config: %w", w.Name(), err)
	}

	fmt.Fprintf(os.Stdout, "Configured %s\n", w.Name())
	fmt.Fprintf(os.Stdout, "Config:    %s\n", selectedConfig.Name)
	fmt.Fprintf(os.Stdout, "MCP URL:   %s\n", mcpURL)
	fmt.Fprintln(os.Stdout)
	return nil
}

// selectConfig fetches configs and either returns the one matching setupConfigName,
// or prompts the user to choose.
func selectConfig(client *api.Client) (*api.MCPConfig, error) {
	if setupConfigName != "" {
		detail, err := client.GetConfig(context.Background(), setupConfigName)
		if err != nil {
			return nil, fmt.Errorf("config %q not found: %w", setupConfigName, err)
		}
		return &detail.MCPConfig, nil
	}

	resp, err := client.ListConfigs(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to list configs: %w", err)
	}
	if len(resp.Configs) == 0 {
		return nil, fmt.Errorf("no MCP configurations available — create one at https://telara.dev")
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
		return nil, fmt.Errorf("selection cancelled: %w", err)
	}
	for i, name := range options {
		if name == chosen {
			return &resp.Configs[i], nil
		}
	}
	return nil, fmt.Errorf("selection not found")
}

// runInteractiveSetup detects installed tools, asks which to configure, then
// runs the setup for each selected tool.
func runInteractiveSetup() error {
	detected := agent.DetectedWriters()
	if len(detected) == 0 {
		return fmt.Errorf("no supported agent tools detected — install Claude Code, Cursor, Windsurf, or VS Code first")
	}

	chosen, err := pickTools(detected)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		fmt.Fprintln(os.Stdout, "No tools selected.")
		return nil
	}

	// Build a lookup map from name → writer.
	byName := make(map[string]agent.AgentWriter, len(detected))
	for _, w := range detected {
		byName[w.Name()] = w
	}

	for _, name := range chosen {
		w := byName[name]
		fmt.Fprintf(os.Stdout, "\nSetting up %s...\n", name)
		scope := agent.ScopeGlobal
		if name == "vscode" {
			scope = agent.ScopeProject
		}
		if err := runSetupForWriter(w, scope); err != nil {
			fmt.Fprintf(os.Stderr, "Error configuring %s: %v\n", name, err)
			continue
		}
		if scope == agent.ScopeProject {
			cwd, err := os.Getwd()
			if err == nil {
				if err := agent.RegisterProject(cwd, name); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to register project for %s: %v\n", name, err)
				}
			}
		}
	}
	return nil
}

// pickTools presents a Select-loop so the user navigates with arrow keys,
// toggles each tool with Enter, and confirms via a "Done" option at the bottom.
func pickTools(detected []agent.AgentWriter) ([]string, error) {
	selected := make(map[string]bool)

	for {
		// Build option list: "[x] name" or "[ ] name" for each tool, then "Done".
		options := make([]string, 0, len(detected)+1)
		for _, w := range detected {
			prefix := "[ ]"
			if selected[w.Name()] {
				prefix = "[x]"
			}
			options = append(options, prefix+" "+w.Name())
		}

		nSelected := 0
		for _, v := range selected {
			if v {
				nSelected++
			}
		}
		if nSelected == 0 {
			options = append(options, "Done")
		} else {
			options = append(options, fmt.Sprintf("Done  (%d selected)", nSelected))
		}

		var pick string
		if err := survey.AskOne(&survey.Select{
			Message: "Select tools to configure:",
			Options: options,
		}, &pick); err != nil {
			return nil, fmt.Errorf("selection cancelled: %w", err)
		}

		if strings.HasPrefix(pick, "Done") {
			break
		}

		// Strip the "[x] " / "[ ] " prefix (4 chars) to get the tool name.
		name := pick[4:]
		selected[name] = !selected[name]
	}

	// Return tools in detection order.
	var chosen []string
	for _, w := range detected {
		if selected[w.Name()] {
			chosen = append(chosen, w.Name())
		}
	}
	return chosen, nil
}

// --- per-tool subcommands ---

var setupClaudeCodeCmd = &cobra.Command{
	Use:   "claude-code",
	Short: "Give Claude Code access to your codebase, tickets, and docs via Telara",
	RunE: func(cmd *cobra.Command, args []string) error {
		w := agent.NewClaudeCodeWriter()
		scope := agent.ScopeGlobal
		if setupManaged {
			scope = agent.ScopeManaged
		}
		if err := runSetupForWriter(w, scope); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Restart Claude Code to connect.")
		return nil
	},
}

var setupCursorCmd = &cobra.Command{
	Use:   "cursor",
	Short: "Give Cursor access to your codebase, tickets, and docs via Telara",
	RunE: func(cmd *cobra.Command, args []string) error {
		w := agent.NewCursorWriter()
		if err := runSetupForWriter(w, agent.ScopeGlobal); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Restart Cursor to connect.")
		return nil
	},
}

var setupWindsurfCmd = &cobra.Command{
	Use:   "windsurf",
	Short: "Give Windsurf access to your codebase, tickets, and docs via Telara",
	RunE: func(cmd *cobra.Command, args []string) error {
		w := agent.NewWindsurfWriter()
		if err := runSetupForWriter(w, agent.ScopeGlobal); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Restart Windsurf to connect.")
		return nil
	},
}

var setupVSCodeCmd = &cobra.Command{
	Use:   "vscode",
	Short: "Give VS Code access to your codebase, tickets, and docs via Telara (project scope)",
	RunE: func(cmd *cobra.Command, args []string) error {
		w := agent.NewVSCodeWriter()
		// VS Code only supports project-scope MCP config.
		if err := runSetupForWriter(w, agent.ScopeProject); err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err == nil {
			if err := agent.RegisterProject(cwd, "vscode"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to register project: %v\n", err)
			}
		}
		fmt.Fprintln(os.Stdout, "Reload VS Code to connect.")
		return nil
	},
}

var setupAllCmd = &cobra.Command{
	Use:   "all",
	Short: "Give all detected agentic coding tools access to your engineering knowledge base",
	RunE: func(cmd *cobra.Command, args []string) error {
		detected := agent.DetectedWriters()
		if len(detected) == 0 {
			return fmt.Errorf("no supported agent tools detected")
		}
		for _, w := range detected {
			fmt.Fprintf(os.Stdout, "\nSetting up %s...\n", w.Name())
			scope := agent.ScopeGlobal
			if w.Name() == "vscode" {
				scope = agent.ScopeProject
			}
			if err := runSetupForWriter(w, scope); err != nil {
				fmt.Fprintf(os.Stderr, "Error configuring %s: %v\n", w.Name(), err)
				continue
			}
			if scope == agent.ScopeProject {
				cwd, err := os.Getwd()
				if err == nil {
					if err := agent.RegisterProject(cwd, w.Name()); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to register project for %s: %v\n", w.Name(), err)
					}
				}
			}
		}
		return nil
	},
}

func init() {
	// Persistent --config flag shared by all setup subcommands.
	setupCmd.PersistentFlags().StringVar(&setupConfigName, "config", "", "MCP configuration name or ID")
	setupCmd.PersistentFlags().BoolVar(&setupManaged, "managed", false, "Write managed-layer config (requires elevated permissions)")

	setupCmd.AddCommand(setupClaudeCodeCmd)
	setupCmd.AddCommand(setupCursorCmd)
	setupCmd.AddCommand(setupWindsurfCmd)
	setupCmd.AddCommand(setupVSCodeCmd)
	setupCmd.AddCommand(setupAllCmd)
	rootCmd.AddCommand(setupCmd)
}
