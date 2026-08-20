package cmd

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelaraConnectPluginIsRemoteAndKeyless(t *testing.T) {
	pluginRoot := filepath.Join("..", "..", "..", "plugins", "telara-connect")
	data, err := os.ReadFile(filepath.Join(pluginRoot, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	telara, ok := manifest.MCPServers["telara"]
	if !ok {
		t.Fatal("plugin does not define the telara MCP server")
	}
	if telara.Type != "http" || telara.URL != "https://api.telara.dev/v1/mcp" {
		t.Fatalf("unexpected Telara MCP target: %#v", telara)
	}
	if len(telara.Headers) != 0 {
		t.Fatalf("Connect Telara plugin must not contain credentials: %#v", telara.Headers)
	}

	logo, err := os.Open(filepath.Join(pluginRoot, "assets", "telara-connector-512.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer logo.Close()
	config, err := png.DecodeConfig(logo)
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 512 || config.Height != 512 {
		t.Fatalf("connector logo must be 512x512, got %dx%d", config.Width, config.Height)
	}
}

func TestCursorMarketplaceUsesTheSameKeylessConnector(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	pluginRoot := filepath.Join(repoRoot, "plugins", "telara-connect")

	marketplaceData, err := os.ReadFile(filepath.Join(repoRoot, ".cursor-plugin", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marketplace struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(marketplaceData, &marketplace); err != nil {
		t.Fatal(err)
	}
	if len(marketplace.Plugins) != 1 || marketplace.Plugins[0].Name != "telara-connect" || marketplace.Plugins[0].Source != "plugins/telara-connect" {
		t.Fatalf("unexpected Cursor marketplace entry: %#v", marketplace.Plugins)
	}

	pluginData, err := os.ReadFile(filepath.Join(pluginRoot, ".cursor-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Name       string          `json:"name"`
		Variables  json.RawMessage `json:"variables"`
		MCPServers string          `json:"mcpServers"`
	}
	if err := json.Unmarshal(pluginData, &plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.Name != "telara-connect" || plugin.MCPServers != "./.mcp.json" {
		t.Fatalf("unexpected Cursor plugin manifest: %#v", plugin)
	}
	if len(plugin.Variables) != 0 {
		t.Fatalf("Cursor plugin must not ask the employee for credentials or tenant configuration: %s", plugin.Variables)
	}
}

func TestTelaraPluginIncludesThePublicOperationalSkill(t *testing.T) {
	pluginRoot := filepath.Join("..", "..", "..", "plugins", "telara-connect")
	skillPath := filepath.Join(pluginRoot, "skills", "telara", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	skill := string(data)
	for _, required := range []string{
		"name: telara",
		"telara_knowledge_search",
		"telara_execute_action",
		"telara_tool_search",
		"telara_tool_describe",
		"telara://integrations/available",
		"Do not ask the user for an API key, MCP URL, tenant ID",
	} {
		if !strings.Contains(skill, required) {
			t.Fatalf("Telara public skill is missing %q", required)
		}
	}
	if strings.Contains(skill, "run_action") {
		t.Fatal("Telara public skill must use the canonical telara_execute_action tool")
	}
}
