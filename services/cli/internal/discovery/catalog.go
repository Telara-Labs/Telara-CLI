package discovery

import "sort"

// catalogFileSource is one local configuration source that the CLI can scan.
//
// This registry is intentionally owned by the CLI. Endpoint discovery must be
// buildable from a public checkout, so it cannot depend on a private catalog
// module or on a network fetch during go mod tidy. Keep entries here in sync
// with the file formats the CLI actually supports; service-only discovery
// modes belong to their owning service, not to this binary.
type catalogFileSource struct {
	IntegrationName string
	ClientID        string
	Scope           string
	ConfigPaths     []string
	Resources       []aiEstateResourceSpec
}

// aiEstateResourceSpec is the small, closed subset of the integration
// catalog's resource contract executed by this CLI: local files emit a client
// configuration record and zero or more MCP server deployment records.
type aiEstateResourceSpec struct {
	Kind             string
	ItemsPath        string
	IDField          string
	IDFallbackFields []string
	IDFallbackPrefix string
	Fields           map[string][]string
	ValueMap         map[string]map[string]string
}

// fileSources returns the CLI-owned, offline discovery registry. A fresh copy
// makes the immutable definitions safe for tests and future callers.
func fileSources() ([]catalogFileSource, error) {
	sources := cliFileSources()
	sortCatalogFileSources(sources)
	return sources, nil
}

func sortCatalogFileSources(sources []catalogFileSource) {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].ClientID != sources[j].ClientID {
			return sources[i].ClientID < sources[j].ClientID
		}
		return sources[i].Scope < sources[j].Scope
	})
}

func cliFileSources() []catalogFileSource {
	return []catalogFileSource{
		mcpObjectSource("amazon_q", ClientAmazonQ, ScopeGlobal, []string{"~/.aws/amazonq/mcp.json"}, "mcpServers"),
		mcpObjectSource("amazon_q", ClientAmazonQ, ScopeProject, []string{".amazonq/mcp.json"}, "mcpServers"),
		mcpObjectSource("claude_code", ClientClaudeCode, ScopeGlobal, []string{"~/.claude.json"}, "mcpServers"),
		mcpObjectSource("claude_code", ClientClaudeCode, ScopeProject, []string{".mcp.json"}, "mcpServers"),
		mcpObjectSource("claude_code", ClientClaudeCode, ScopeManaged, []string{"/Library/Application Support/ClaudeCode/managed-mcp.json", "/etc/claude-code/managed-mcp.json", "C:\\ProgramData\\ClaudeCode\\managed-mcp.json"}, "mcpServers"),
		mcpObjectSource("cline", "cline", ScopeGlobal, []string{"~/.cline/mcp.json"}, "mcpServers"),
		mcpListSource("continue_dev", "continue_dev", ScopeGlobal, []string{"~/.continue/config.yaml"}, "mcpServers", "name"),
		mcpObjectSource("cursor", ClientCursor, ScopeGlobal, []string{"~/.cursor/mcp.json"}, "mcpServers"),
		mcpObjectSource("cursor", ClientCursor, ScopeProject, []string{".cursor/mcp.json"}, "mcpServers"),
		mcpObjectSource("factory", "factory", ScopeGlobal, []string{"~/.factory/mcp.json"}, "mcpServers"),
		mcpObjectSource("factory", "factory", ScopeProject, []string{".factory/mcp.json"}, "mcpServers"),
		mcpObjectSource("gemini_code_assist", ClientGemini, ScopeGlobal, []string{"~/.gemini/settings.json"}, "mcpServers"),
		mcpObjectSource("gemini_code_assist", ClientGemini, ScopeProject, []string{".gemini/settings.json"}, "mcpServers"),
		mcpObjectSource("openai_codex", ClientCodex, ScopeGlobal, []string{"~/.codex/config.toml"}, "mcp_servers"),
		mcpObjectSource("openai_codex", ClientCodex, ScopeProject, []string{".codex/config.toml"}, "mcp_servers"),
		mcpObjectSource("sourcegraph_cody", "amp", ScopeGlobal, []string{"~/.config/amp/settings.json", "~/.config/amp/settings.jsonc"}, "amp.mcpServers"),
		mcpObjectSource("sourcegraph_cody", "amp", ScopeProject, []string{".amp/settings.json", ".amp/settings.jsonc"}, "amp.mcpServers"),
		mcpObjectSource("windsurf", ClientWindsurf, ScopeGlobal, []string{"~/.codeium/windsurf/mcp_config.json"}, "mcpServers"),
		mcpObjectSource("windsurf", ClientWindsurf, ScopeProject, []string{".windsurf/mcp_config.json"}, "mcpServers"),
		mcpObjectSource("vscode", ClientVSCode, ScopeProject, []string{".vscode/mcp.json"}, "servers"),
	}
}

func mcpObjectSource(integration, client, scope string, paths []string, itemsPath string) catalogFileSource {
	return mcpSource(integration, client, scope, paths, itemsPath, "_key")
}

func mcpListSource(integration, client, scope string, paths []string, itemsPath, idField string) catalogFileSource {
	return mcpSource(integration, client, scope, paths, itemsPath, idField)
}

func mcpSource(integration, client, scope string, paths []string, itemsPath, idField string) catalogFileSource {
	return catalogFileSource{
		IntegrationName: integration,
		ClientID:        client,
		Scope:           scope,
		ConfigPaths:     append([]string(nil), paths...),
		Resources: []aiEstateResourceSpec{
			{
				Kind:    "mcp_client_configuration",
				IDField: "_path",
				Fields: map[string][]string{
					"client": {"_client_id"},
					"scope":  {"_scope"},
				},
			},
			{
				Kind:      "mcp_server_deployment",
				ItemsPath: itemsPath,
				IDField:   idField,
				Fields: map[string][]string{
					"name":      {"_key"},
					"command":   {"command", "args"},
					"transport": {"type"},
				},
				ValueMap: map[string]map[string]string{
					"transport": {"": "stdio"},
				},
			},
		},
	}
}
