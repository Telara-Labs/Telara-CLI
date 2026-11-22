package discovery

import (
	"path/filepath"
	"testing"
)

// Goose's user config is discovered: extensions keyed by name, cmd/envs for a
// stdio server and uri/headers for a streamable_http one (TENG-3921).
func TestGooseConfigIsDiscovered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	writeFixture(t, filepath.Join(home, ".config", "goose", "config.yaml"), `extensions:
  gdrive:
    cmd: npx
    args: [-y, gdrive-mcp]
    envs: {GOOGLE_REFRESH_TOKEN: "1//0gTok"}
    type: stdio
  calendar:
    type: streamable_http
    uri: https://cal.example.test/mcp
`)
	byName := map[string]DiscoveredServer{}
	for _, r := range ScanAll() {
		if r.ClientFamily != ClientGoose {
			continue
		}
		for _, s := range r.Servers {
			byName[s.ServerName] = s
		}
	}
	if s := byName["gdrive"]; s.Transport != TransportStdio || s.CommandIdentity == "" || s.CredentialClass == "" {
		t.Fatalf("gdrive %+v", s)
	}
	if s := byName["calendar"]; s.Transport != TransportHTTP || s.EndpointHost != "https://cal.example.test" {
		t.Fatalf("calendar %+v", s)
	}
}

func TestCredentialYAMLFilesComeFromTheRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got := map[string]MCPConfigFile{}
	for _, f := range CredentialYAMLFiles("/home/u") {
		got[f.Path] = f
	}
	want := map[string]MCPConfigFile{
		filepath.Join("/home/u", ".continue", "config.yaml"):        {ClientID: "continue_dev", ItemsKey: "mcpServers", IDField: "name"},
		filepath.Join("/home/u", ".config", "goose", "config.yaml"): {ClientID: ClientGoose, ItemsKey: "extensions", IDField: "_key"},
		filepath.Join("/xdg", "goose", "config.yaml"):               {ClientID: ClientGoose, ItemsKey: "extensions", IDField: "_key"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for p, w := range want {
		g := got[p]
		if g.ClientID != w.ClientID || g.ItemsKey != w.ItemsKey || g.IDField != w.IDField {
			t.Fatalf("%s: %+v", p, g)
		}
	}
}
