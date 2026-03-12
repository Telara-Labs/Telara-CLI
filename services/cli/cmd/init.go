package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/agent"
)

var initConfigName string
var initToolFlag string

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write project-scope MCP config for detected (or specified) agent tools",
	Long: `telara init writes MCP server configuration into project-local config files
(e.g. .claude/settings.json, .cursor/mcp.json) in the current directory.

Use --tool to target specific tools; omit it to configure all detected tools.
Use --config to select an MCP configuration by name or ID without prompting.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve the set of writers to use.
		var writers []agent.AgentWriter
		if initToolFlag != "" {
			// User specified one or more tools.
			toolNames := strings.Split(initToolFlag, ",")
			byName := make(map[string]agent.AgentWriter)
			for _, w := range agent.AllWriters() {
				byName[w.Name()] = w
			}
			for _, name := range toolNames {
				name = strings.TrimSpace(name)
				w, ok := byName[name]
				if !ok {
					return fmt.Errorf("unknown tool %q — supported: claude-code, cursor, windsurf, vscode", name)
				}
				writers = append(writers, w)
			}
		} else {
			writers = agent.DetectedWriters()
			if len(writers) == 0 {
				return fmt.Errorf("no supported agent tools detected — install Claude Code, Cursor, Windsurf, or VS Code first, or use --tool to specify one")
			}
		}

		// Override the global setupConfigName so selectConfig picks it up.
		setupConfigName = initConfigName

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}

		for _, w := range writers {
			fmt.Fprintf(os.Stdout, "Configuring %s (project scope)...\n", w.Name())
			if err := runSetupForWriter(w, agent.ScopeProject); err != nil {
				fmt.Fprintf(os.Stderr, "Error configuring %s: %v\n", w.Name(), err)
				continue
			}
			if err := agent.RegisterProject(cwd, w.Name()); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to register project for %s: %v\n", w.Name(), err)
			}
		}

		// Print gitignore advice.
		fmt.Fprintln(os.Stdout, "Project config files written. Add to .gitignore to avoid committing API keys:")
		fmt.Fprintln(os.Stdout, "  .claude/settings.json")
		fmt.Fprintln(os.Stdout, "  .cursor/mcp.json")
		fmt.Fprintln(os.Stdout, "  .vscode/mcp.json")
		fmt.Fprintln(os.Stdout, "  .windsurf/mcp_config.json")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initConfigName, "config", "", "MCP configuration name or ID")
	initCmd.Flags().StringVar(&initToolFlag, "tool", "", "Comma-separated list of tools to configure (e.g. claude-code,cursor)")
	rootCmd.AddCommand(initCmd)
}
