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
	w := agent.NewCodexWriter()
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpURLForWriter(tt.in, w); got != tt.want {
				t.Fatalf("mcpURLForWriter(%q, codex) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMCPURLForWriter_SSEClientsKeepSSEEndpoint(t *testing.T) {
	w := agent.NewClaudeCodeWriter()
	in := "https://api.telara.dev/v1/mcp/sse"
	if got := mcpURLForWriter(in, w); got != in {
		t.Fatalf("mcpURLForWriter(%q, claude-code) = %q, want unchanged", in, got)
	}
}

func TestMCPEntryMatchesWriterEndpoint(t *testing.T) {
	codex := agent.NewCodexWriter()
	stale := agent.MCPEntry{URL: "https://api.telara.dev/v1/mcp/sse"}
	current := agent.MCPEntry{URL: "https://api.telara.dev/v1/mcp"}

	if mcpEntryMatchesWriterEndpoint(codex, stale) {
		t.Fatal("expected stale Codex SSE URL to need endpoint repair")
	}
	if !mcpEntryMatchesWriterEndpoint(codex, current) {
		t.Fatal("expected Codex streamable URL to match")
	}
}
