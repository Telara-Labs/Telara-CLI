package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
)

func init() {
	rootCmd.AddCommand(otlpCmd)
	otlpCmd.Flags().Bool("print-only", false, "Print env exports without minting a new key (requires --api-key)")
	otlpCmd.Flags().String("api-key", "", "Existing telara_mcp_... key to embed in the printed exports")
	otlpCmd.Flags().String("name", "", "Name for a newly minted MCP API key (default: otlp-<hostname>)")
}

var otlpCmd = &cobra.Command{
	Use:   "otlp",
	Short: "Configure OpenTelemetry export to Telara (Claude Code, Cline, frameworks)",
	Long: `Prints the OTLP endpoint and Authorization header for this tenant.

Governance Connect stores org admin credentials only. Realtime OTLP capture
(Claude Code, Cowork via admin portal, Cline, telara-observability) is configured
here — typically via MDM managed settings or shell profile.

Examples:
  telara otlp                     Mint a tenant MCP key and print export lines
  telara otlp --api-key telara_mcp_...   Re-print exports for an existing key

After printing, point Claude Code managed settings (or OTEL_* env) at the
endpoint. Cowork / Office agents: paste the same endpoint + Bearer header in
the Anthropic admin portal (org-wide).`,
	RunE: runOTLP,
}

func defaultOTLPEndpoint() string {
	return otlpEndpointFromAPIURL(prefs.APIURL)
}

func otlpEndpointFromAPIURL(apiURL string) string {
	base := strings.TrimRight(apiURL, "/")
	// Prefer dedicated otlp host when API is api.telara.dev; otherwise same origin.
	if strings.Contains(base, "://api.") {
		return strings.Replace(base, "://api.", "://otlp.", 1)
	}
	return base
}

func runOTLP(cmd *cobra.Command, args []string) error {
	printOnly, _ := cmd.Flags().GetBool("print-only")
	existingKey, _ := cmd.Flags().GetString("api-key")
	keyName, _ := cmd.Flags().GetString("name")

	endpoint := defaultOTLPEndpoint()
	var rawKey string

	if existingKey != "" {
		rawKey = existingKey
	} else if printOnly {
		return fmt.Errorf("--print-only requires --api-key")
	} else {
		tok, err := auth.LoadToken(prefs.APIURL)
		if err != nil || tok == "" {
			return fmt.Errorf("not logged in — run 'telara login' first")
		}
		client := api.NewClient(prefs.APIURL, tok)

		cfg, err := selectConfigInteractive(client)
		if err != nil {
			return err
		}
		dep, err := selectDeployment(client, cfg.ID)
		if err != nil {
			return err
		}
		if keyName == "" {
			keyName = toolKeyName("otlp")
		}
		keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
			Name:      keyName,
			ScopeType: dep.ScopeType,
			ScopeID:   dep.ScopeID,
		})
		if err != nil {
			return fmt.Errorf("failed to mint MCP API key: %w", err)
		}
		rawKey = keyResp.RawKey
		fmt.Fprintf(os.Stderr, "Minted MCP API key %s (shown once below — store it securely).\n\n", keyResp.Prefix)
	}

	snippet := otlpExportSnippet(endpoint, rawKey)
	fmt.Fprint(os.Stdout, snippet)
	fmt.Fprint(os.Stderr, `
# Also:
# - Claude Code MDM managed settings: set the same OTEL_* keys (users cannot override).
# - Cowork / Office agents: Admin settings → paste OTLP endpoint + Authorization header.
# - Frameworks: pip install 'telara-observability[...]' then instrument(..., api_key=..., endpoint=...).
`)
	return nil
}

func otlpExportSnippet(endpoint, rawKey string) string {
	return strings.Join([]string{
		"export CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"export OTEL_METRICS_EXPORTER=otlp",
		"export OTEL_LOGS_EXPORTER=otlp",
		"export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		fmt.Sprintf("export OTEL_EXPORTER_OTLP_ENDPOINT=%s", endpoint),
		fmt.Sprintf(`export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer %s"`, rawKey),
		"",
	}, "\n")
}
