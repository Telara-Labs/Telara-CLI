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

// ---------------------------------------------------------------------------
// config keys <name-or-id>
// ---------------------------------------------------------------------------

var configKeysCmd = &cobra.Command{
	Use:   "keys <name-or-id>",
	Short: "List API keys for an MCP configuration",
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
			t.AddRow(k.ID, k.Name, k.Prefix, "", k.CreatedAt, expires, status)
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
	Short: "Generate a new API key for an MCP configuration",
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

		fmt.Fprintln(os.Stdout, "Key generated.")
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "WARNING: Save this key now — it will not be shown again.")
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintf(os.Stdout, "  Key ID:  %s\n", keyResp.KeyID)
		fmt.Fprintf(os.Stdout, "  Prefix:  %s\n", keyResp.Prefix)
		fmt.Fprintf(os.Stdout, "  Key:     %s\n", keyResp.RawKey)
		fmt.Fprintf(os.Stdout, "  MCP URL: %s\n", keyResp.MCPURL)
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
	Short: "Revoke an API key",
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
		fmt.Fprintf(os.Stdout, "Key %s revoked.\n", args[0])
		return nil
	},
}

// ---------------------------------------------------------------------------
// config rotate-key <context-name>
// ---------------------------------------------------------------------------

var configRotateKeyCmd = &cobra.Command{
	Use:   "rotate-key <context-name>",
	Short: "Rotate the API key for a saved context",
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

		fmt.Fprintln(os.Stdout, "New key generated.")
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "WARNING: Save this key now — it will not be shown again.")
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintf(os.Stdout, "  Key ID:  %s\n", keyResp.KeyID)
		fmt.Fprintf(os.Stdout, "  Prefix:  %s\n", keyResp.Prefix)
		fmt.Fprintf(os.Stdout, "  Key:     %s\n", keyResp.RawKey)
		fmt.Fprintf(os.Stdout, "  MCP URL: %s\n", keyResp.MCPURL)
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "Update your MCP config files with the new key, then run:")
		fmt.Fprintf(os.Stdout, "  telara config revoke-key %s --config %s\n", oldKeyID, c.ConfigID)
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
