package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanDiscoversStdioCommandIdentity(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Chdir(tempDir)
	writeFixture(t, filepath.Join(tempDir, ".cursor", "mcp.json"), `{
		"mcpServers": {
			"github": {
				"command": "/usr/local/bin/npx",
				"args": ["-y", "@modelcontextprotocol/server-github"]
			}
		}
	}`)

	result := Scan(ClientCursor, ScopeGlobal)
	requireStatus(t, result, ScanOK)
	if len(result.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result.Servers))
	}
	server := result.Servers[0]
	if server.Transport != TransportStdio {
		t.Fatalf("expected stdio transport, got %q", server.Transport)
	}
	if server.CommandIdentity != "npx:-y:@modelcontextprotocol/server-github" {
		t.Fatalf("unexpected command identity: %q", server.CommandIdentity)
	}
	if server.RawArgCount != 2 {
		t.Fatalf("expected raw arg count 2, got %d", server.RawArgCount)
	}
}

func TestScanRemoteEndpointHostStripsPathQueryAndUserinfo(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	writeFixture(t, filepath.Join(tempDir, ".gemini", "settings.json"), `{
		"mcpServers": {
			"remote": {
				"url": "https://user:secret@example.com:8443/mcp/sse?api_key=sk-live-abc123#frag"
			}
		}
	}`)

	result := Scan(ClientGemini, ScopeGlobal)
	requireStatus(t, result, ScanOK)
	if len(result.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result.Servers))
	}
	server := result.Servers[0]
	if server.EndpointHost != "https://example.com:8443" {
		t.Fatalf("unexpected endpoint host: %q", server.EndpointHost)
	}
	serialized := mustJSON(t, result)
	for _, forbidden := range []string{"api_key", "sk-live-abc123", "/mcp/sse", "user:secret"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("serialized result leaked %q: %s", forbidden, serialized)
		}
	}
}

func TestInlineCredentialsReturnHintOnly(t *testing.T) {
	tests := []struct {
		name     string
		entry    string
		secret   string
		wantHint string
	}{
		{
			name: "header",
			entry: `"headers": {
				"Authorization": "Bearer sk-live-abc123"
			}`,
			secret:   "sk-live-abc123",
			wantHint: "Authorization",
		},
		{
			name: "env",
			entry: `"env": {
				"GITHUB_TOKEN": "ghp_liveabc123456789"
			}`,
			secret:   "ghp_liveabc123456789",
			wantHint: "GITHUB_TOKEN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("HOME", tempDir)
			writeFixture(t, filepath.Join(tempDir, ".claude.json"), `{
				"mcpServers": {
					"secretful": {
						"url": "https://example.com/mcp",
						`+tt.entry+`
					}
				}
			}`)

			result := Scan(ClientClaudeCode, ScopeGlobal)
			requireStatus(t, result, ScanOK)
			if len(result.Servers) != 1 {
				t.Fatalf("expected 1 server, got %d", len(result.Servers))
			}
			server := result.Servers[0]
			if server.CredentialClass != CredentialInline {
				t.Fatalf("expected CredentialInline, got %q", server.CredentialClass)
			}
			if server.CredentialHint != tt.wantHint {
				t.Fatalf("expected hint %q, got %q", tt.wantHint, server.CredentialHint)
			}
			serialized := mustJSON(t, result)
			if strings.Contains(serialized, tt.secret) {
				t.Fatalf("serialized result leaked secret %q: %s", tt.secret, serialized)
			}
		})
	}
}

func TestEnvReferenceCredential(t *testing.T) {
	credentialClass, hint := ClassifyCredential(nil, map[string]string{"GITHUB_TOKEN": "${TOKEN}"})
	if credentialClass != CredentialEnvReferenced {
		t.Fatalf("expected CredentialEnvReferenced, got %q", credentialClass)
	}
	if hint != "GITHUB_TOKEN" {
		t.Fatalf("expected GITHUB_TOKEN hint, got %q", hint)
	}
}

func TestMalformedJSONIsParseFailed(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	writeFixture(t, filepath.Join(tempDir, ".cursor", "mcp.json"), `{"mcpServers": {`)

	result := Scan(ClientCursor, ScopeGlobal)
	if result.Status == ScanOK {
		t.Fatalf("expected non-OK status for malformed JSON, got %q", result.Status)
	}
	if result.Status != ScanParseFailed {
		t.Fatalf("expected ScanParseFailed, got %q", result.Status)
	}
	if len(result.Servers) != 0 {
		t.Fatalf("expected no servers on parse failure, got %d", len(result.Servers))
	}
}

func TestMissingFileIsFirstClassAbsent(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	result := Scan(ClientCursor, ScopeGlobal)
	if result.Status != ScanFileAbsent {
		t.Fatalf("expected ScanFileAbsent, got %q", result.Status)
	}
	if result.ErrorClass != "" {
		t.Fatalf("expected empty error class, got %q", result.ErrorClass)
	}
	if len(result.Servers) != 0 {
		t.Fatalf("expected no servers, got %d", len(result.Servers))
	}
}

func TestDiscoveryReturnsTelaraAndThirdPartyEntries(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	writeFixture(t, filepath.Join(tempDir, ".aws", "amazonq", "mcp.json"), `{
		"mcpServers": {
			"telara": {
				"type": "http",
				"url": "https://mcp.telara.example/mcp"
			},
			"github": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"]
			}
		}
	}`)

	result := Scan(ClientAmazonQ, ScopeGlobal)
	requireStatus(t, result, ScanOK)
	if len(result.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(result.Servers))
	}
	names := map[string]bool{}
	for _, server := range result.Servers {
		names[server.ServerName] = true
	}
	for _, want := range []string{"telara", "github"} {
		if !names[want] {
			t.Fatalf("missing discovered server %q in %#v", want, result.Servers)
		}
	}
}

func TestPathClassDoesNotExposeHomePathOrUsername(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	path := filepath.Join(tempDir, ".claude", "settings.json")

	class := PathClass(path)
	if strings.Contains(class, tempDir) {
		t.Fatalf("path class exposed home path: %q", class)
	}
	if user := filepath.Base(tempDir); strings.Contains(class, user) {
		t.Fatalf("path class exposed username/path component %q: %q", user, class)
	}
	if class != "user_global:claude_code" {
		t.Fatalf("unexpected path class: %q", class)
	}
}

func requireStatus(t *testing.T, result ConfigScanResult, want ScanStatus) {
	t.Helper()
	if result.Status != want {
		t.Fatalf("expected status %q, got %q: %#v", want, result.Status, result)
	}
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return string(data)
}

func writeFixture(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
