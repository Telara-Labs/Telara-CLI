package cmd

import (
	"net/url"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
)

func defaultMCPURL() string {
	return strings.TrimRight(prefs.APIURL, "/") + "/v1/mcp/sse"
}

// streamableDefaultMCPURL returns the streamable-HTTP MCP endpoint (no /sse
// suffix). Used by non-interactive provisioning, whose targets — Claude.ai org
// connectors, CI runners, and MDM-managed Claude Code — all speak streamable
// HTTP, not the legacy SSE transport.
func streamableDefaultMCPURL() string {
	return streamableMCPURL(defaultMCPURL())
}

func mcpURLForWriter(mcpURL string, w agent.AgentWriter) string {
	if streamableMCPWriter(w) {
		return streamableMCPURL(mcpURL)
	}
	return mcpURL
}

func mcpTypeForWriter(w agent.AgentWriter) string {
	if streamableMCPWriter(w) {
		return "http"
	}
	return "sse"
}

func mcpEntryMatchesWriterEndpoint(w agent.AgentWriter, entry agent.MCPEntry) bool {
	return entry.URL != "" &&
		entry.URL == mcpURLForWriter(entry.URL, w) &&
		entry.Type == mcpTypeForWriter(w)
}

func streamableMCPWriter(w agent.AgentWriter) bool {
	// Every supported client now speaks the streamable-HTTP MCP transport, so we
	// emit `type: http` + the /v1/mcp endpoint for all of them. The legacy SSE
	// transport is no longer written: its split GET-stream + separate message
	// POST dropped the auth header on the POST, causing intermittent 401s.
	return w != nil
}

func normalizeMCPEntryForWriter(entry agent.MCPEntry, w agent.AgentWriter) agent.MCPEntry {
	entry.URL = mcpURLForWriter(entry.URL, w)
	entry.Type = mcpTypeForWriter(w)
	return entry
}

func newMCPEntryForWriter(mcpURL, rawKey string, w agent.AgentWriter) agent.MCPEntry {
	return agent.MCPEntry{
		Type:    mcpTypeForWriter(w),
		URL:     mcpURLForWriter(mcpURL, w),
		Headers: map[string]string{"Authorization": "Bearer " + rawKey},
	}
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
