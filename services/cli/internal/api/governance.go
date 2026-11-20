package api

import "context"

// MCPServerBlocklist returns the tenant's mcp_server_blocklist setting (a JSON
// array of server names) and whether it is set (TENG-3921).
//
// It reads GET /v1/cli/mcp-servers/blocked, which accepts the CLI token. The
// dashboard route /v1/governance/settings accepts only a user JWT and refuses a
// CLI token with 401, so a machine could never read the list from there.
func (c *Client) MCPServerBlocklist(ctx context.Context) (string, bool, error) {
	var resp struct {
		Blocklist string `json:"blocklist"`
		Set       bool   `json:"set"`
	}
	if err := c.do(ctx, "GET", "/v1/cli/mcp-servers/blocked", nil, &resp); err != nil {
		return "", false, err
	}
	return resp.Blocklist, resp.Set, nil
}
