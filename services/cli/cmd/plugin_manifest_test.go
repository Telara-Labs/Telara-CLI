package cmd

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
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
