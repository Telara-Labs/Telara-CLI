package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

var configCmd = &cobra.Command{
	Use:   "config [name-or-id]",
	Short: "Manage which MCP configurations your AI tools connect to",
	Long: `Telara connects your AI tools in three layers — each overrides the one below:

  Layer 1 · Managed     Set by your admin, applied on login. Always active.
  Layer 2 · Global      Your personal choice, applied across all projects.
  Layer 3 · Project     Per-directory override for a specific repo.

Common commands:
  telara config                    Show what's configured at each layer
  telara config list               Browse available configurations
  telara config show <name>        Inspect a configuration's data sources and policies
  telara config global <name>      Set your global (Layer 2) configuration
  telara config project <name>     Set a project (Layer 3) override for this directory`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			// telara config <name> → shorthand for config project <name>
			return runConfigProject(args[0])
		}
		// telara config → show status
		return runConfigStatus()
	},
}

// ---------------------------------------------------------------------------
// config list
// ---------------------------------------------------------------------------

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List knowledge access profiles you can connect to",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAuthenticatedClient()
		if err != nil {
			return err
		}
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
			scope := c.ScopeName
			if scope == "" {
				scope = c.ScopeType
			}
			t.AddRow(c.Name, scope, fmt.Sprintf("%d", c.DataSources), c.Status)
		}
		t.Print(os.Stdout)
		return nil
	},
}

// ---------------------------------------------------------------------------
// config show <name-or-id>
// ---------------------------------------------------------------------------

var configShowCmd = &cobra.Command{
	Use:   "show <name-or-id>",
	Short: "Show data sources, deployments, policies, and keys for a knowledge access profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAuthenticatedClient()
		if err != nil {
			return err
		}
		cfg, err := client.GetConfig(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		w := os.Stdout

		// ── Header ────────────────────────────────────────────────
		display.PrintKV(w, "Name:", cfg.Name)
		display.PrintKV(w, "ID:", cfg.ID)
		if cfg.Description != "" {
			display.PrintKV(w, "Description:", cfg.Description)
		}
		scope := cfg.ScopeName
		if scope == "" {
			scope = cfg.ScopeType
		}
		display.PrintKV(w, "Scope:", scope)
		display.PrintKV(w, "Status:", cfg.Status)
		if cfg.MCPURL != "" {
			display.PrintKVHighlight(w, "MCP URL:", cfg.MCPURL)
		}
		fmt.Fprintln(w)

		// ── Data Sources ──────────────────────────────────────────
		display.PrintSection("Data Sources")
		if len(cfg.DataSources) == 0 {
			display.PrintInfo("No data sources configured.")
		} else {
			t := &display.Table{Headers: []string{"INTEGRATION", "CREDENTIAL", "MODE"}}
			for _, ds := range cfg.DataSources {
				name := ds.Name
				if name == "" {
					name = "—"
				}
				mode := ds.SelectionMode
				if mode == "" {
					mode = "all"
				}
				t.AddRow(ds.Integration, name, mode)
			}
			t.Print(w)
		}
		fmt.Fprintln(w)

		// ── Deployments ───────────────────────────────────────────
		display.PrintSection("Deployments")
		deployments := cfg.Deployments
		if len(deployments) == 0 && cfg.DeploymentCount > 0 {
			depResp, err := client.ListDeployments(context.Background(), cfg.ID)
			if err == nil {
				deployments = depResp.Deployments
			}
		}
		if len(deployments) == 0 {
			display.PrintInfo("Not deployed to any scope.")
		} else {
			t := &display.Table{Headers: []string{"SCOPE", "TARGET", "DEFAULT"}}
			for _, d := range deployments {
				target := d.ScopeName
				if target == "" {
					target = "(tenant-wide)"
				}
				def := ""
				if d.IsDefault {
					def = "✓"
				}
				t.AddRow(d.ScopeType, target, def)
			}
			t.Print(w)
		}
		fmt.Fprintln(w)

		// ── Keys & Policies ───────────────────────────────────────
		display.PrintSection("Access")
		display.PrintKV(w, "API Keys:", fmt.Sprintf("%d", cfg.KeyCount))
		display.PrintKV(w, "Policies:", fmt.Sprintf("%d", cfg.PolicyCount))

		return nil
	},
}

// ---------------------------------------------------------------------------
// config keys <name-or-id>
// ---------------------------------------------------------------------------

var configKeysCmd = &cobra.Command{
	Use:   "keys <name-or-id>",
	Short: "List active API keys issued for a knowledge access profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAuthenticatedClient()
		if err != nil {
			return err
		}

		configID, err := resolveConfigID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.ListKeys(context.Background(), configID)
		if err != nil {
			return fmt.Errorf("list keys: %w", err)
		}
		if len(resp.Keys) == 0 {
			fmt.Fprintln(os.Stdout, "No API keys found for this configuration.")
			return nil
		}

		t := &display.Table{Headers: []string{"NAME", "PREFIX", "SCOPE", "CREATED", "EXPIRES", "STATUS"}}
		for _, k := range resp.Keys {
			status := "active"
			if k.Revoked {
				status = "revoked"
			}
			expires := k.ExpiresAt
			if expires == "" {
				expires = "never"
			}
			scope := k.ScopeType
			if k.ScopeID != "" {
				scope = k.ScopeType + "/" + k.ScopeID
			}
			t.AddRow(k.Name, k.Prefix, scope, k.CreatedAt, expires, status)
		}
		t.Print(os.Stdout)
		return nil
	},
}

// ---------------------------------------------------------------------------
// config global <name-or-id>
// ---------------------------------------------------------------------------

var configGlobalCmd = &cobra.Command{
	Use:   "global [name-or-id]",
	Short: "Set the MCP configuration used by all your AI tools (Layer 2)",
	Long: `Detects your installed AI tools (Claude Code, Cursor, Windsurf, VS Code, Codex, Gemini CLI, Amazon Q)
and writes the selected MCP configuration to each tool's global settings.
This becomes your default across all projects.

If no name is given, you'll be prompted to select from available configurations.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := ""
		if len(args) == 1 {
			nameOrID = args[0]
		}
		return runConfigGlobal(nameOrID)
	},
}

// ---------------------------------------------------------------------------
// config project <name-or-id>
// ---------------------------------------------------------------------------

var configProjectCmd = &cobra.Command{
	Use:   "project [name-or-id]",
	Short: "Set an MCP configuration for the current directory only (Layer 3)",
	Long: `Writes a project-scoped MCP configuration into the current directory.
When your AI tools open this project, they'll use this configuration instead
of the global one. Useful when different repos need different integrations.

If no name is given, you'll be prompted to select from available configurations.

Remember to add the generated config files to .gitignore:
  .mcp.json  .cursor/mcp.json  .vscode/mcp.json  .windsurf/mcp_config.json
  .codex/config.toml  .gemini/settings.json  .amazonq/mcp.json`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nameOrID := ""
		if len(args) == 1 {
			nameOrID = args[0]
		}
		return runConfigProject(nameOrID)
	},
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

func runConfigGlobal(nameOrID string) error {
	client, err := newAuthenticatedClient()
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(client, nameOrID)
	if err != nil {
		return err
	}
	return wireTools(client, cfg, agent.ScopeGlobal)
}

func runConfigProject(nameOrID string) error {
	client, err := newAuthenticatedClient()
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(client, nameOrID)
	if err != nil {
		return err
	}
	if err := wireTools(client, cfg, agent.ScopeProject); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Add generated config files to .gitignore to avoid committing API keys.")
	return nil
}

func runConfigStatus() error {
	w := os.Stdout

	// Load locally-stored wired config names (written by `telara config global/project`).
	wiredState, _ := agent.LoadWiredState()

	fmt.Fprintln(w)
	display.PrintSection("Configuration Layers")

	// Check each detected tool for telara entries at each scope.
	detected := agent.DetectedWriters()

	type layerInfo struct {
		name       string
		scope      agent.Scope
		found      bool
		configName string
	}
	layers := []layerInfo{
		{name: "Managed", scope: agent.ScopeManaged},
		{name: "Global", scope: agent.ScopeGlobal},
		{name: "Project", scope: agent.ScopeProject},
	}

	for i, l := range layers {
		for _, dw := range detected {
			entries, err := dw.Read(l.scope)
			if err != nil {
				continue
			}
			if entry, ok := entries["telara"]; ok && entry.URL != "" {
				layers[i].found = true
				break
			}
		}
	}

	// Attach config names from wired state.
	if wiredState.Global != nil {
		for i := range layers {
			if layers[i].scope == agent.ScopeGlobal && layers[i].found {
				layers[i].configName = wiredState.Global.ConfigName
			}
		}
	}
	// For project scope, resolve from current directory.
	if wiredState.Projects != nil {
		cwd, _ := os.Getwd()
		if cwd != "" {
			if wc, ok := wiredState.Projects[cwd]; ok {
				for i := range layers {
					if layers[i].scope == agent.ScopeProject && layers[i].found {
						layers[i].configName = wc.ConfigName
					}
				}
			}
		}
	}

	for _, l := range layers {
		if l.found {
			if l.configName != "" {
				fmt.Fprintf(w, "  %-12s %s  %s\n", l.name+":",
					display.ColorSuccess.Sprint("✓"),
					display.ColorBold.Sprint(l.configName))
			} else {
				fmt.Fprintf(w, "  %-12s %s\n", l.name+":", display.ColorSuccess.Sprint("✓  connected"))
			}
		} else {
			fmt.Fprintf(w, "  %-12s %s\n", l.name+":", display.ColorDim.Sprint("—  not configured"))
		}
	}
	fmt.Fprintln(w)

	// Show detected tools.
	if len(detected) > 0 {
		var toolStatus []string
		for _, dw := range detected {
			entries, err := dw.Read(agent.ScopeGlobal)
			if err != nil {
				toolStatus = append(toolStatus, display.ColorDim.Sprint(dw.Name()+" —"))
				continue
			}
			if entry, ok := entries["telara"]; ok && entry.URL != "" {
				toolStatus = append(toolStatus, dw.Name()+" ✓")
			} else {
				toolStatus = append(toolStatus, display.ColorDim.Sprint(dw.Name()+" —"))
			}
		}
		fmt.Fprintf(w, "  Tools: %s\n", strings.Join(toolStatus, "  "))
		fmt.Fprintln(w)
	}

	// Show project paths with their configured MCP config.
	projects, _ := agent.ListProjects()
	if len(projects) > 0 {
		display.PrintSection("Project Configurations")
		t := &display.Table{Headers: []string{"PATH", "MCP CONFIG", "TOOLS", "UPDATED"}}
		for _, p := range projects {
			configName := "—"
			if wiredState.Projects != nil {
				if wc, ok := wiredState.Projects[p.Path]; ok {
					configName = wc.ConfigName
				}
			}
			tools := strings.Join(p.Tools, ", ")
			updated := p.UpdatedAt
			if len(updated) > 10 {
				updated = updated[:10] // just the date
			}
			t.AddRow(shortenPath(p.Path), configName, tools, updated)
		}
		t.Print(w)
		fmt.Fprintln(w)
	}

	// Show available configs if logged in (deduplicated by ID).
	token, err := auth.LoadToken(prefs.APIURL)
	if err == nil {
		client := api.NewClient(prefs.APIURL, token)
		resolved, err := client.ResolveConfigs(context.Background())
		if err == nil {
			all := deduplicateConfigs(resolved)
			if len(all) > 0 {
				display.PrintSection("Available Configurations")
				for _, c := range all {
					scope := c.ScopeName
					if scope == "" {
						scope = c.ScopeType
					}
					fmt.Fprintf(w, "  %-30s %s\n", c.Name, display.ColorDim.Sprint(scope))
				}
				fmt.Fprintln(w)
			}
		}
	}

	display.ShowHints("", []display.ActionHint{
		{Label: "Set global config", Command: []string{"telara", "config", "global", "<name>"}, Description: "telara config global <name>"},
		{Label: "Set project config", Command: []string{"telara", "config", "project", "<name>"}, Description: "telara config project <name>"},
	})
	return nil
}

// deduplicateConfigs merges all resolve buckets and deduplicates by config ID.
// Prefers the entry with the most specific scope info (user > available > managed).
func deduplicateConfigs(resolved *api.ResolveResponse) []api.MCPConfig {
	seen := make(map[string]bool)
	var result []api.MCPConfig

	// User configs first (most specific), then managed, then available.
	for _, c := range resolved.User {
		if !seen[c.ID] {
			seen[c.ID] = true
			result = append(result, c)
		}
	}
	for _, c := range resolved.Managed {
		if !seen[c.ID] {
			seen[c.ID] = true
			result = append(result, c)
		}
	}
	for _, c := range resolved.Available {
		if !seen[c.ID] {
			seen[c.ID] = true
			result = append(result, c)
		}
	}
	return result
}

// shortenPath replaces the user's home directory with ~ for display.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// resolveConfigID returns the config ID for a name-or-id argument.
func resolveConfigID(client *api.Client, nameOrID string) (string, error) {
	if len(nameOrID) == 36 && strings.Count(nameOrID, "-") == 4 {
		return nameOrID, nil
	}
	cfg, err := client.GetConfig(context.Background(), nameOrID)
	if err != nil {
		return "", fmt.Errorf("resolve config %q: %w", nameOrID, err)
	}
	return cfg.ID, nil
}

func init() {
	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configKeysCmd)
	configCmd.AddCommand(configGlobalCmd)
	configCmd.AddCommand(configProjectCmd)
	rootCmd.AddCommand(configCmd)
}
