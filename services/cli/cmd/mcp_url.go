package cmd

import (
	"net/url"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
)

func defaultMCPURL() string {
	return strings.TrimRight(prefs.APIURL, "/") + "/v1/mcp/sse"
}

func mcpURLForWriter(mcpURL string, w agent.AgentWriter) string {
	if w != nil && w.Name() == "codex" {
		return streamableMCPURL(mcpURL)
	}
	return mcpURL
}

func mcpEntryMatchesWriterEndpoint(w agent.AgentWriter, entry agent.MCPEntry) bool {
	return entry.URL != "" && entry.URL == mcpURLForWriter(entry.URL, w)
}

func streamableMCPURL(mcpURL string) string {
	if parsed, err := url.Parse(mcpURL); err == nil {
		if path := stripSSEFromMCPPath(parsed.Path); path != parsed.Path {
			parsed.Path = path
			parsed.RawPath = ""
			return parsed.String()
		}
	}

	if path := stripSSEFromMCPPath(mcpURL); path != mcpURL {
		return path
	}
	return mcpURL
}

func stripSSEFromMCPPath(path string) string {
	base := strings.TrimSuffix(path, "/sse")
	if base != path && strings.HasSuffix(base, "/mcp") {
		return base
	}
	return path
}
