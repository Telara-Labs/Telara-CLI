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
	Short: "Manage server-side access profiles (integrations, tools, and policies)",
	Long: `A configuration defines what your AI tool can see and do: which integrations
are connected (GitHub, Jira, Confluence, Slack, etc.), which tools and actions
are exposed, and which governance policies apply. Scoped to a tenant, team, or
project. Managed in the Telara web app — use this command to inspect, key, and
rotate credentials.`,
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List knowledge access profiles you can connect to",
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
	Short: "Show data sources, policies, and keys for a knowledge access profile",
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
		display.PrintKV(os.Stdout, "Name:", cfg.Name)
		display.PrintKV(os.Stdout, "ID:", cfg.ID)
		display.PrintKV(os.Stdout, "Scope:", cfg.ScopeType)
		display.PrintKV(os.Stdout, "Status:", cfg.Status)
		display.PrintKVHighlight(os.Stdout, "MCP URL:", cfg.MCPURL)
		display.PrintKV(os.Stdout, "Policies:", fmt.Sprintf("%d", cfg.PolicyCount))
		display.PrintKV(os.Stdout, "API Keys:", fmt.Sprintf("%d", cfg.KeyCount))
		if len(cfg.DataSources) > 0 {
			names := make([]string, len(cfg.DataSources))
			for i, ds := range cfg.DataSources {
				names[i] = ds.Name + " (" + ds.Integration + ")"
			}
			display.PrintKV(os.Stdout, "Data Sources:", strings.Join(names, ", "))
		} else {
			display.PrintKV(os.Stdout, "Data Sources:", "none")
		}
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
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)

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

		t := &display.Table{Headers: []string{"ID", "NAME", "PREFIX", "SCOPE", "CREATED", "EXPIRES", "STATUS"}}
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
			t.AddRow(k.ID, k.Name, k.Prefix, scope, k.CreatedAt, expires, status)
		}
		t.Print(os.Stdout)
		return nil
	},
}

// ---------------------------------------------------------------------------
// config generate-key <name-or-id>
// ---------------------------------------------------------------------------

var (
	generateKeyName    string
	generateKeyExpires string
)

var configGenerateKeyCmd = &cobra.Command{
	Use:   "generate-key <name-or-id>",
	Short: "Generate a new API key to connect a tool to a knowledge access profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)

		configID, err := resolveConfigID(client, args[0])
		if err != nil {
			return err
		}

		expiresSeconds, err := parseExpiresDuration(generateKeyExpires)
		if err != nil {
			return err
		}

		keyName := generateKeyName
		if keyName == "" {
			keyName = "generated-key"
		}

		keyResp, err := client.GenerateKey(context.Background(), configID, api.GenerateKeyRequest{
			Name:             keyName,
			ExpiresInSeconds: expiresSeconds,
		})
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}

		display.PrintSuccess("Key generated.")
		fmt.Fprintln(os.Stdout)
		display.PrintWarn("Save this key now — it will not be shown again.")
		fmt.Fprintln(os.Stdout)
		display.PrintKV(os.Stdout, "Key ID:", keyResp.KeyID)
		display.PrintKV(os.Stdout, "Prefix:", keyResp.Prefix)
		display.PrintKV(os.Stdout, "Key:", keyResp.RawKey)
		display.PrintKVHighlight(os.Stdout, "MCP URL:", keyResp.MCPURL)
		return nil
	},
}

// parseExpiresDuration converts a human duration string to seconds.
// Supported: "30d", "90d", "1yr", "never" (0 = no expiry).
func parseExpiresDuration(s string) (int, error) {
	switch strings.ToLower(s) {
	case "", "never":
		return 0, nil
	case "30d":
		return 2592000, nil
	case "90d":
		return 7776000, nil
	case "1yr", "1y":
		return 31536000, nil
	default:
		return 0, fmt.Errorf("unsupported --expires value %q: use 30d, 90d, 1yr, or never", s)
	}
}

// ---------------------------------------------------------------------------
// config revoke-key <key-id> --config <config-id>
// ---------------------------------------------------------------------------

var revokeKeyConfig string

var configRevokeKeyCmd = &cobra.Command{
	Use:   "revoke-key <key-id>",
	Short: "Revoke an API key (disconnects any tool using it immediately)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if revokeKeyConfig == "" {
			return fmt.Errorf("--config is required: provide the config ID that owns this key")
		}
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)

		if err := client.RevokeKey(context.Background(), args[0], revokeKeyConfig); err != nil {
			return fmt.Errorf("revoke key: %w", err)
		}
		display.PrintSuccess(fmt.Sprintf("Key %s revoked.", args[0]))
		return nil
	},
}

// ---------------------------------------------------------------------------
// config rotate-key <context-name>
// ---------------------------------------------------------------------------

var configRotateKeyCmd = &cobra.Command{
	Use:   "rotate-key <context-name>",
	Short: "Replace the API key for a saved connection without re-running setup",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName := args[0]

		store, err := newContextStore()
		if err != nil {
			return fmt.Errorf("open context store: %w", err)
		}
		c, err := store.Get(contextName)
		if err != nil {
			return err
		}

		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}
		client := api.NewClient(prefs.APIURL, token)

		newKeyName := c.ConfigName + "-" + contextName + "-rotated"
		keyResp, err := client.GenerateKey(context.Background(), c.ConfigID, api.GenerateKeyRequest{
			Name:      newKeyName,
			ScopeType: c.ScopeType,
			ScopeID:   c.ScopeID,
		})
		if err != nil {
			return fmt.Errorf("generate replacement key: %w", err)
		}

		oldKeyID := c.APIKeyID

		// Update context store with new key metadata.
		c.APIKeyID = keyResp.KeyID
		c.APIKeyPrefix = keyResp.Prefix
		c.MCPURL = keyResp.MCPURL
		if err := store.Save(*c); err != nil {
			return fmt.Errorf("update context store: %w", err)
		}

		// Revoke the old key now that the context store is updated.
		if err := client.RevokeKey(context.Background(), oldKeyID, c.ConfigID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: new key generated but failed to revoke old key %s: %v\n", oldKeyID, err)
			fmt.Fprintf(os.Stderr, "Revoke it manually: telara config revoke-key %s --config %s\n", oldKeyID, c.ConfigID)
		}

		display.PrintSuccess("New key generated. Old key revoked.")
		fmt.Fprintln(os.Stdout)
		display.PrintWarn("Save this key now — it will not be shown again.")
		fmt.Fprintln(os.Stdout)
		display.PrintKV(os.Stdout, "Key ID:", keyResp.KeyID)
		display.PrintKV(os.Stdout, "Prefix:", keyResp.Prefix)
		display.PrintKV(os.Stdout, "Key:", keyResp.RawKey)
		display.PrintKVHighlight(os.Stdout, "MCP URL:", keyResp.MCPURL)
		return nil
	},
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// resolveConfigID returns the config ID for a name-or-id argument.
// If the argument looks like a UUID (contains '-' and is long), it is used directly.
// Otherwise the config list is fetched and matched by name.
func resolveConfigID(client *api.Client, nameOrID string) (string, error) {
	// Heuristic: UUIDs are 36 chars and contain hyphens.
	if len(nameOrID) == 36 && strings.Count(nameOrID, "-") == 4 {
		return nameOrID, nil
	}
	// Fetch by name via the detail endpoint — the API accepts both name and ID.
	cfg, err := client.GetConfig(context.Background(), nameOrID)
	if err != nil {
		return "", fmt.Errorf("resolve config %q: %w", nameOrID, err)
	}
	return cfg.ID, nil
}

func init() {
	configGenerateKeyCmd.Flags().StringVar(&generateKeyName, "name", "", "Key name")
	configGenerateKeyCmd.Flags().StringVar(&generateKeyExpires, "expires", "never", "Expiry: 30d, 90d, 1yr, never")

	configRevokeKeyCmd.Flags().StringVar(&revokeKeyConfig, "config", "", "Config ID that owns the key (required)")

	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configKeysCmd)
	configCmd.AddCommand(configGenerateKeyCmd)
	configCmd.AddCommand(configRevokeKeyCmd)
	configCmd.AddCommand(configRotateKeyCmd)
	rootCmd.AddCommand(configCmd)
}
