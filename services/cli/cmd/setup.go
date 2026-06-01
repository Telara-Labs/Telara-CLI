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
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/clicontext"
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
		return fmt.Errorf("no supported agent tools detected — install Claude Code, Cursor, Windsurf, VS Code, Codex, Gemini CLI, or Amazon Q first")
	}

	keyName := toolKeyName(writers[0].Name())
	keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
		Name:      keyName,
		ScopeType: dep.ScopeType,
		ScopeID:   dep.ScopeID,
	})
	if err != nil {
		return fmt.Errorf("failed to generate API key: %w", err)
	}

	mcpURL := keyResp.MCPURL
	if mcpURL == "" {
		mcpURL = defaultMCPURL()
	}

	var wired []string
	for _, w := range writers {
		entry := newMCPEntryForWriter(mcpURL, keyResp.RawKey, w)
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

	// Replace, don't accumulate: revoke any prior key with the same name for
	// this config so repeated `telara setup`/`config` runs on this machine
	// don't pile up dead duplicate keys server-side. The raw key can't be
	// re-fetched, so we mint a fresh one (written above) and retire the rest.
	revokeSupersededKeys(client, cfg.ID, keyName, keyResp.KeyID)

	// Persist which config was wired so `telara config` can show names.
	switch scope {
	case agent.ScopeGlobal:
		_ = agent.SaveWiredGlobal(cfg.ID, cfg.Name)
	case agent.ScopeProject:
		cwd, err := os.Getwd()
		if err == nil {
			_ = agent.SaveWiredProject(cwd, cfg.ID, cfg.Name)
			for _, name := range wired {
				_ = agent.RegisterProject(cwd, name)
			}
		}
	}

	// Auto-save a context so CLI diagnostic commands (doctor, whoami) can
	// verify the key and display the active configuration.
	// Global scope also sets the active context preference; project scope does not
	// (project config is directory-specific and looked up via wired-state.json).
	if store, err := newContextStore(); err == nil {
		entry := clicontext.Context{
			Name:         cfg.Name,
			ConfigID:     cfg.ID,
			ConfigName:   cfg.Name,
			ScopeType:    dep.ScopeType,
			ScopeID:      dep.ScopeID,
			APIKeyID:     keyResp.KeyID,
			APIKeyPrefix: keyResp.Prefix,
			MCPURL:       mcpURL,
		}
		if err := store.Save(entry); err == nil && scope == agent.ScopeGlobal {
			_ = store.SetActive(cfg.Name)
		}
	}

	fmt.Fprintf(os.Stdout, "Config:  %s\n", cfg.Name)
	fmt.Fprintf(os.Stdout, "Tools:   %s\n", strings.Join(wired, ", "))
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Start a new session to connect.")
	return nil
}

// revokeSupersededKeys retires prior, still-active keys that share keyName for
// this config (excluding the one just minted). This keeps exactly one live key
// per machine+tool instead of leaving a trail of orphaned credentials behind on
// every re-run. Best-effort: a listing or revoke failure is non-fatal since the
// fresh key is already wired.
func revokeSupersededKeys(client *api.Client, configID, keyName, keepKeyID string) {
	if keyName == "" {
		return
	}
	resp, err := client.ListKeys(context.Background(), configID)
	if err != nil {
		return
	}
	for _, k := range resp.Keys {
		if k.Revoked || k.ID == keepKeyID || k.Name != keyName {
			continue
		}
		if err := client.RevokeKey(context.Background(), k.ID, configID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to revoke superseded key %s: %v\n", k.Prefix, err)
		}
	}
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
