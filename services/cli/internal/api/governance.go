package api

import "context"

// MCPServerBlocklist returns the tenant's mcp_server_blocklist setting (a JSON
// array of server names) and whether it is set (TENG-3921).
//
// It reads GET /v1/cli/mcp-servers/blocked, which accepts the CLI token. The
// dashboard route /v1/governance/settings accepts only a user JWT and refuses a
// CLI token with 401, so a machine could never read the list from there.
//
// managedOnly is the tenant's managed-devices-only setting: when true, a device
// without a managed policy leaves its MCP client files alone.
func (c *Client) MCPServerBlocklist(ctx context.Context) (value string, set bool, managedOnly bool, err error) {
	var resp struct {
		Blocklist   string `json:"blocklist"`
		Set         bool   `json:"set"`
		ManagedOnly bool   `json:"managed_devices_only"`
	}
	if err := c.do(ctx, "GET", "/v1/cli/mcp-servers/blocked", nil, &resp); err != nil {
		return "", false, false, err
	}
	return resp.Blocklist, resp.Set, resp.ManagedOnly, nil
}
