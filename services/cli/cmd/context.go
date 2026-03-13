package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	survey "github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/clicontext"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/display"
)

// ---------------------------------------------------------------------------
// Root context command
// ---------------------------------------------------------------------------

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Manage locally saved connections (config + API key) to your Telara workspace",
	Long: `A context pairs a Telara configuration with a locally stored API key — like a
kubectl context. Switch between them to change which integrations and tools your
AI assistant can reach. The active context is what 'telara setup' and 'telara
init' write into your editor's config file.`,
}

// ---------------------------------------------------------------------------
// context list
// ---------------------------------------------------------------------------

var contextListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved connections and which one is active",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := newContextStore()
		if err != nil {
			return fmt.Errorf("open context store: %w", err)
		}
		ctxs, err := store.List()
		if err != nil {
			return fmt.Errorf("list contexts: %w", err)
		}
		if len(ctxs) == 0 {
			fmt.Fprintln(os.Stdout, "No contexts saved. Run: telara context create <name>")
			return nil
		}

		active := clicontext.Resolve(rootContext, prefs.ActiveContext)

		t := &display.Table{Headers: []string{"NAME", "CONFIG", "SCOPE", "KEY PREFIX", "ACTIVE"}}
		for _, c := range ctxs {
			scope := c.ScopeType
			if c.ScopeID != "" {
				scope = c.ScopeType + "/" + c.ScopeID
			}
			marker := ""
			if c.Name == active {
				marker = "*"
			}
			t.AddRow(c.Name, c.ConfigName, scope, c.APIKeyPrefix, marker)
		}
		t.Print(os.Stdout)
		return nil
	},
}

// ---------------------------------------------------------------------------
// context create
// ---------------------------------------------------------------------------

var (
	contextCreateConfig string
	contextCreateScope  string
)

var contextCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Save a new named connection (generates an API key for the chosen profile)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)

		// Resolve config: if --config not supplied, prompt interactively.
		configIDOrName := contextCreateConfig
		if configIDOrName == "" {
			resp, err := client.ListConfigs(context.Background())
			if err != nil {
				return fmt.Errorf("fetch configs: %w", err)
			}
			if len(resp.Configs) == 0 {
				return fmt.Errorf("no MCP configurations available — create one first")
			}
			options := make([]string, len(resp.Configs))
			for i, c := range resp.Configs {
				options[i] = c.Name
			}
			var chosen string
			if err := survey.AskOne(&survey.Select{
				Message: "Select MCP configuration:",
				Options: options,
			}, &chosen); err != nil {
				return fmt.Errorf("prompt cancelled: %w", err)
			}
			configIDOrName = chosen
		}

		// Fetch config detail to get canonical ID, name, and MCP URL.
		cfg, err := client.GetConfig(context.Background(), configIDOrName)
		if err != nil {
			return fmt.Errorf("get config %q: %w", configIDOrName, err)
		}

		// Parse scope flag (format: "tenant", "team/id", "project/id", "user").
		scopeType, scopeID := parseScopeFlag(contextCreateScope, cfg.ScopeType, cfg.ScopeID)

		// Generate API key.
		keyReq := api.GenerateKeyRequest{
			Name:      name + "-context",
			ScopeType: scopeType,
			ScopeID:   scopeID,
		}
		keyResp, err := client.GenerateKey(context.Background(), cfg.ID, keyReq)
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}

		// Persist context (without raw key).
		store, err := newContextStore()
		if err != nil {
			return fmt.Errorf("open context store: %w", err)
		}
		newCtx := clicontext.Context{
			Name:         name,
			ConfigID:     cfg.ID,
			ConfigName:   cfg.Name,
			ScopeType:    scopeType,
			ScopeID:      scopeID,
			APIKeyID:     keyResp.KeyID,
			APIKeyPrefix: keyResp.Prefix,
			MCPURL:       keyResp.MCPURL,
		}
		if err := store.Save(newCtx); err != nil {
			return fmt.Errorf("save context: %w", err)
		}

		display.PrintSuccess("Context created: " + name)
		fmt.Fprintln(os.Stdout)
		display.PrintWarn("Save this key now — it will not be shown again.")
		fmt.Fprintln(os.Stdout)
		display.PrintKV(os.Stdout, "Key:", keyResp.RawKey)
		display.PrintKVHighlight(os.Stdout, "MCP URL:", keyResp.MCPURL)
		return nil
	},
}

// parseScopeFlag splits a scope flag value ("tenant", "team/abc123", etc.) into
// (scopeType, scopeID).  Falls back to the config's own scope values when the
// flag is empty.
func parseScopeFlag(flag, defaultType, defaultID string) (string, string) {
	if flag == "" {
		return defaultType, defaultID
	}
	parts := strings.SplitN(flag, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// ---------------------------------------------------------------------------
// context use
// ---------------------------------------------------------------------------

var contextUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Switch which connection your AI tools use",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := newContextStore()
		if err != nil {
			return fmt.Errorf("open context store: %w", err)
		}
		if err := store.SetActive(args[0]); err != nil {
			return err
		}
		display.PrintSuccess(fmt.Sprintf("Switched to context %q.", args[0]))
		return nil
	},
}

// ---------------------------------------------------------------------------
// context current
// ---------------------------------------------------------------------------

var contextCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show which connection is currently active",
	RunE: func(cmd *cobra.Command, args []string) error {
		name := clicontext.Resolve(rootContext, prefs.ActiveContext)
		if name == "" {
			fmt.Fprintln(os.Stdout, "No active context set. Run: telara context use <name>")
			return nil
		}
		store, err := newContextStore()
		if err != nil {
			return fmt.Errorf("open context store: %w", err)
		}
		c, err := store.Get(name)
		if err != nil {
			return err
		}
		scope := c.ScopeType
		if c.ScopeID != "" {
			scope = c.ScopeType + "/" + c.ScopeID
		}
		display.PrintKV(os.Stdout, "Name:", c.Name)
		display.PrintKV(os.Stdout, "Config:", fmt.Sprintf("%s (%s)", c.ConfigName, c.ConfigID))
		display.PrintKV(os.Stdout, "Scope:", scope)
		display.PrintKV(os.Stdout, "Key Prefix:", c.APIKeyPrefix)
		display.PrintKV(os.Stdout, "Key ID:", c.APIKeyID)
		display.PrintKVHighlight(os.Stdout, "MCP URL:", c.MCPURL)
		return nil
	},
}

// ---------------------------------------------------------------------------
// context delete
// ---------------------------------------------------------------------------

var contextDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Remove a saved connection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		store, err := newContextStore()
		if err != nil {
			return fmt.Errorf("open context store: %w", err)
		}

		// Confirm if the context is currently active.
		active := clicontext.Resolve(rootContext, prefs.ActiveContext)
		if name == active {
			var confirmed bool
			if err := survey.AskOne(&survey.Confirm{
				Message: fmt.Sprintf("Context %q is currently active. Delete anyway?", name),
				Default: false,
			}, &confirmed); err != nil {
				return fmt.Errorf("prompt cancelled: %w", err)
			}
			if !confirmed {
				fmt.Fprintln(os.Stdout, "Aborted.")
				return nil
			}
		}

		c, getErr := store.Get(name)
		if err := store.Delete(name); err != nil {
			return err
		}
		display.PrintSuccess(fmt.Sprintf("Context %q deleted.", name))
		if getErr == nil && c.APIKeyID != "" {
			display.PrintWarn(fmt.Sprintf("API key %s (%s) is still active on the platform.", c.APIKeyID, c.APIKeyPrefix))
			fmt.Fprintf(os.Stdout, "  To revoke it: telara config revoke-key %s --config %s\n", c.APIKeyID, c.ConfigID)
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	contextCreateCmd.Flags().StringVar(&contextCreateConfig, "config", "", "MCP config name or ID")
	contextCreateCmd.Flags().StringVar(&contextCreateScope, "scope", "", "Scope override: tenant, team/<id>, project/<id>, user")

	contextCmd.AddCommand(contextListCmd)
	contextCmd.AddCommand(contextCreateCmd)
	contextCmd.AddCommand(contextUseCmd)
	contextCmd.AddCommand(contextCurrentCmd)
	contextCmd.AddCommand(contextDeleteCmd)
	rootCmd.AddCommand(contextCmd)
}
