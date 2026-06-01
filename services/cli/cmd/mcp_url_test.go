package cmd

import (
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

func TestDefaultMCPURL(t *testing.T) {
	orig := prefs
	prefs = &config.Prefs{APIURL: "https://api.telara.dev/"}
	defer func() { prefs = orig }()

	if got, want := defaultMCPURL(), "https://api.telara.dev/v1/mcp/sse"; got != want {
		t.Fatalf("defaultMCPURL() = %q, want %q", got, want)
	}
}

func TestMCPURLForWriter_CodexUsesStreamableHTTP(t *testing.T) {
	writers := map[string]agent.AgentWriter{
		"claude-code": agent.NewClaudeCodeWriter(),
		"codex":       agent.NewCodexWriter(),
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ingress sse endpoint",
			in:   "https://api.telara.dev/v1/mcp/sse",
			want: "https://api.telara.dev/v1/mcp",
		},
		{
			name: "direct gateway sse endpoint",
			in:   "http://localhost:8080/mcp/sse",
			want: "http://localhost:8080/mcp",
		},
		{
			name: "preserves query",
			in:   "https://api.telara.dev/v1/mcp/sse?tenant=demo",
			want: "https://api.telara.dev/v1/mcp?tenant=demo",
		},
		{
			name: "already streamable",
			in:   "https://api.telara.dev/v1/mcp",
			want: "https://api.telara.dev/v1/mcp",
		},
		{
			name: "unrelated sse path",
			in:   "https://api.telara.dev/custom/sse",
			want: "https://api.telara.dev/custom/sse",
		},
	}

	for writerName, w := range writers {
		for _, tt := range tests {
			t.Run(writerName+"/"+tt.name, func(t *testing.T) {
				if got := mcpURLForWriter(tt.in, w); got != tt.want {
					t.Fatalf("mcpURLForWriter(%q, %s) = %q, want %q", tt.in, writerName, got, tt.want)
				}
			})
		}
	}
}

func TestMCPURLForWriter_SSEClientsKeepSSEEndpoint(t *testing.T) {
	w := agent.NewCursorWriter()
	in := "https://api.telara.dev/v1/mcp/sse"
	if got := mcpURLForWriter(in, w); got != in {
		t.Fatalf("mcpURLForWriter(%q, cursor) = %q, want unchanged", in, got)
	}
}

func TestMCPEntryMatchesWriterEndpoint(t *testing.T) {
	codex := agent.NewCodexWriter()
	staleURL := agent.MCPEntry{Type: "http", URL: "https://api.telara.dev/v1/mcp/sse"}
	staleType := agent.MCPEntry{Type: "sse", URL: "https://api.telara.dev/v1/mcp"}
	current := agent.MCPEntry{Type: "http", URL: "https://api.telara.dev/v1/mcp"}

	if mcpEntryMatchesWriterEndpoint(codex, staleURL) {
		t.Fatal("expected stale Codex SSE URL to need endpoint repair")
	}
	if mcpEntryMatchesWriterEndpoint(codex, staleType) {
		t.Fatal("expected stale Codex SSE type to need endpoint repair")
	}
	if !mcpEntryMatchesWriterEndpoint(codex, current) {
		t.Fatal("expected Codex streamable URL to match")
	}
}

func TestNewMCPEntryForWriterUsesTransportForAgent(t *testing.T) {
	rawKey := "secret"
	mcpURL := "https://api.telara.dev/v1/mcp/sse"

	tests := []struct {
		name     string
		writer   agent.AgentWriter
		wantType string
		wantURL  string
	}{
		{
			name:     "claude-code",
			writer:   agent.NewClaudeCodeWriter(),
			wantType: "http",
			wantURL:  "https://api.telara.dev/v1/mcp",
		},
		{
			name:     "codex",
			writer:   agent.NewCodexWriter(),
			wantType: "http",
			wantURL:  "https://api.telara.dev/v1/mcp",
		},
		{
			name:     "cursor",
			writer:   agent.NewCursorWriter(),
			wantType: "sse",
			wantURL:  "https://api.telara.dev/v1/mcp/sse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := newMCPEntryForWriter(mcpURL, rawKey, tt.writer)
			if entry.Type != tt.wantType {
				t.Fatalf("entry.Type = %q, want %q", entry.Type, tt.wantType)
			}
			if entry.URL != tt.wantURL {
				t.Fatalf("entry.URL = %q, want %q", entry.URL, tt.wantURL)
			}
			if got, want := entry.Headers["Authorization"], "Bearer "+rawKey; got != want {
				t.Fatalf("Authorization header = %q, want %q", got, want)
			}
		})
	}
}
