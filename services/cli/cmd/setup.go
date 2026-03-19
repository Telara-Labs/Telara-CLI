package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
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

// selectConfigInteractive fetches configs and prompts the user to choose one.
func selectConfigInteractive(client *api.Client) (*api.MCPConfig, error) {
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

// wireTools generates a key for the given config and writes the MCP entry
// to all detected tools at the given scope. Keys are generated and stored
// internally — the user never sees them.
func wireTools(client *api.Client, cfg *api.MCPConfig, scope agent.Scope) error {
	dep, err := selectDeployment(client, cfg.ID)
	if err != nil {
		return err
	}

	writers := agent.DetectedWriters()
	if len(writers) == 0 {
		return fmt.Errorf("no supported agent tools detected — install Claude Code, Cursor, Windsurf, or VS Code first")
	}

	keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
		Name:      toolKeyName(writers[0].Name()),
		ScopeType: dep.ScopeType,
		ScopeID:   dep.ScopeID,
	})
	if err != nil {
		return fmt.Errorf("failed to generate API key: %w", err)
	}

	mcpURL := keyResp.MCPURL
	if mcpURL == "" {
		mcpURL = prefs.APIURL + "/v1/mcp/sse"
	}

	entry := agent.MCPEntry{
		Type:    "sse",
		URL:     mcpURL,
		Headers: map[string]string{"Authorization": "Bearer " + keyResp.RawKey},
	}

	var wired []string
	for _, w := range writers {
		if err := w.Write(scope, "telara", entry); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to configure %s: %v\n", w.Name(), err)
			continue
		}
		if pw, ok := w.(agent.PermissionWriter); ok {
			_ = pw.WritePermissions(scope, "telara")
		}
		wired = append(wired, w.Name())
	}

	if len(wired) == 0 {
		return fmt.Errorf("failed to configure any tools")
	}

	// Register project for project-scope wiring.
	if scope == agent.ScopeProject {
		cwd, err := os.Getwd()
		if err == nil {
			for _, name := range wired {
				_ = agent.RegisterProject(cwd, name)
			}
		}
	}

	fmt.Fprintf(os.Stdout, "Config:  %s\n", cfg.Name)
	fmt.Fprintf(os.Stdout, "Tools:   %s\n", strings.Join(wired, ", "))
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Restart your tools to connect.")
	return nil
}

// resolveConfig resolves a config by name-or-id, or prompts interactively.
func resolveConfig(client *api.Client, nameOrID string) (*api.MCPConfig, error) {
	if nameOrID == "" {
		return selectConfigInteractive(client)
	}
	detail, err := client.GetConfig(context.Background(), nameOrID)
	if err != nil {
		return nil, fmt.Errorf("config %q not found: %w", nameOrID, err)
	}
	return &detail.MCPConfig, nil
}

// newAuthenticatedClient creates an API client from the stored token.
func newAuthenticatedClient() (*api.Client, error) {
	token, err := auth.LoadToken(prefs.APIURL)
	if err != nil {
		return nil, fmt.Errorf("not logged in — run: telara login")
	}
	return api.NewClient(prefs.APIURL, token), nil
}
