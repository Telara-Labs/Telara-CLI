package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// FetchToolNames asks the MCP server which tools it actually serves.
//
// Cursor, Windsurf and Codex auto-approve by explicit tool name, so a stale
// hardcoded list silently re-prompts the user on every call to a tool the
// server gained since the CLI was built. Asking the server removes the copy
// rather than correcting it — PlatformToolNames stays only as the offline
// fallback, pinned by a test.
func FetchToolNames(ctx context.Context, mcpURL, bearer string) ([]string, error) {
	if mcpURL == "" || bearer == "" {
		return nil, fmt.Errorf("an MCP URL and credential are required to list tools")
	}
	// The SSE endpoint is the connect URL; tools/list is served by the
	// Streamable HTTP endpoint beside it.
	endpoint := strings.TrimSuffix(strings.TrimSuffix(mcpURL, "/"), "/sse")

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tools/list returned HTTP %d", response.StatusCode)
	}

	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("tools/list returned an unreadable body: %w", err)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("tools/list failed: %s", envelope.Error.Message)
	}
	if len(envelope.Result.Tools) == 0 {
		return nil, fmt.Errorf("tools/list returned no tools")
	}

	names := make([]string, 0, len(envelope.Result.Tools))
	for _, tool := range envelope.Result.Tools {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ResolveToolNames returns the server's live tool list, falling back to the
// built-in names when the server cannot be reached. Setup must not fail because
// auto-approval could not be personalised.
func ResolveToolNames(ctx context.Context, mcpURL, bearer string) []string {
	if names, err := FetchToolNames(ctx, mcpURL, bearer); err == nil {
		return names
	}
	return PlatformToolNames()
}
