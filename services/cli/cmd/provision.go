package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
)

var (
	provisionConfigName     string
	provisionServiceAccount string
	provisionOutputPath     string
)

var provisionCmd = &cobra.Command{
	Use:   "provision",
	Short: "Generate MCP access keys for specific deployment scenarios",
}

var provisionClaudeWebCmd = &cobra.Command{
	Use:   "claude-web",
	Short: "Generate MCP key for Claude.ai web (Anthropic Organization Connector)",
	RunE:  runProvisionClaudeWeb,
}

var provisionCICmd = &cobra.Command{
	Use:   "ci",
	Short: "Generate MCP key for CI/CD environments",
	RunE:  runProvisionCI,
}

var provisionManagedCmd = &cobra.Command{
	Use:   "managed",
	Short: "Generate managed-mcp.json for enterprise MDM/GPO deployment",
	RunE:  runProvisionManaged,
}

func provisionClient() (*api.Client, error) {
	token, err := auth.LoadToken(prefs.APIURL)
	if err != nil {
		return nil, fmt.Errorf("not logged in — run: telara login")
	}
	return api.NewClient(prefs.APIURL, token), nil
}

func provisionSelectConfig(client *api.Client) (*api.MCPConfig, error) {
	if provisionConfigName != "" {
		detail, err := client.GetConfig(context.Background(), provisionConfigName)
		if err != nil {
			return nil, fmt.Errorf("config %q not found: %w", provisionConfigName, err)
		}
		return &detail.MCPConfig, nil
	}
	return selectConfig(client)
}

func runProvisionClaudeWeb(cmd *cobra.Command, args []string) error {
	client, err := provisionClient()
	if err != nil {
		return err
	}

	cfg, err := provisionSelectConfig(client)
	if err != nil {
		return err
	}

	keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
		Name:      "claude-web-org-connector",
		ScopeType: "tenant",
	})
	if err != nil {
		return fmt.Errorf("failed to generate API key: %w", err)
	}

	mcpURL := keyResp.MCPURL
	if mcpURL == "" {
		mcpURL = "https://mcp.telara.dev/sse"
	}

	fmt.Fprintln(os.Stdout, "Claude.ai Organization Connector Setup")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "MCP Endpoint:  %s\n", mcpURL)
	fmt.Fprintf(os.Stdout, "API Key:       %s\n", keyResp.RawKey)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "In Anthropic Admin Console:")
	fmt.Fprintln(os.Stdout, "  1. Go to Settings > Connectors")
	fmt.Fprintln(os.Stdout, "  2. Add MCP connector with the above URL and key")
	return nil
}

func runProvisionCI(cmd *cobra.Command, args []string) error {
	client, err := provisionClient()
	if err != nil {
		return err
	}

	cfg, err := provisionSelectConfig(client)
	if err != nil {
		return err
	}

	keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
		Name:      provisionServiceAccount,
		ScopeType: "tenant",
	})
	if err != nil {
		return fmt.Errorf("failed to generate API key: %w", err)
	}

	mcpURL := keyResp.MCPURL
	if mcpURL == "" {
		mcpURL = "https://mcp.telara.dev/sse"
	}

	fmt.Fprintln(os.Stdout, "CI/CD Service Account Key")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Add this to your CI environment:")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "  export TELARA_MCP_URL=%q\n", mcpURL)
	fmt.Fprintf(os.Stdout, "  export TELARA_API_KEY=%q\n", keyResp.RawKey)
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "GitHub Actions:")
	fmt.Fprintln(os.Stdout, "  Add TELARA_API_KEY as a repository secret, then reference with ${{ secrets.TELARA_API_KEY }}")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "GitLab CI:")
	fmt.Fprintln(os.Stdout, "  Add TELARA_API_KEY as a CI/CD variable (masked)")
	return nil
}

func runProvisionManaged(cmd *cobra.Command, args []string) error {
	client, err := provisionClient()
	if err != nil {
		return err
	}

	cfg, err := provisionSelectConfig(client)
	if err != nil {
		return err
	}

	keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
		Name:      "managed-deployment",
		ScopeType: "tenant",
	})
	if err != nil {
		return fmt.Errorf("failed to generate API key: %w", err)
	}

	mcpURL := keyResp.MCPURL
	if mcpURL == "" {
		mcpURL = "https://mcp.telara.dev/sse"
	}

	managed := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"telara": map[string]interface{}{
				"type": "sse",
				"url":  mcpURL,
				"headers": map[string]string{
					"Authorization": "Bearer " + keyResp.RawKey,
				},
			},
		},
	}

	data, err := json.MarshalIndent(managed, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal managed-mcp.json: %w", err)
	}

	if provisionOutputPath == "" || provisionOutputPath == "-" {
		fmt.Fprintln(os.Stdout, string(data))
	} else {
		if err := os.WriteFile(provisionOutputPath, data, 0600); err != nil {
			return fmt.Errorf("failed to write %s: %w", provisionOutputPath, err)
		}
		fmt.Fprintf(os.Stdout, "Written to %s\n", provisionOutputPath)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Deployment instructions:")
	fmt.Fprintln(os.Stdout, "  macOS (MDM):   Deploy to /Library/Application Support/ClaudeCode/managed-mcp.json")
	fmt.Fprintln(os.Stdout, "  Linux (GPO):   Deploy to /etc/claude-code/managed-mcp.json")
	fmt.Fprintln(os.Stdout, "  Windows (GPO): Deploy to C:\\ProgramData\\ClaudeCode\\managed-mcp.json")
	return nil
}

func init() {
	provisionCmd.PersistentFlags().StringVar(&provisionConfigName, "config", "", "MCP configuration name or ID")
	provisionCICmd.Flags().StringVar(&provisionServiceAccount, "service-account", "ci-service-account", "Service account name")
	provisionManagedCmd.Flags().StringVarP(&provisionOutputPath, "output", "o", "-", "Output file path (- for stdout)")

	provisionCmd.AddCommand(provisionClaudeWebCmd, provisionCICmd, provisionManagedCmd)
	rootCmd.AddCommand(provisionCmd)
}
