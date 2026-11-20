package api

import (
	"context"
	"net/url"
)

// Blocklist is what a scan needs to enforce MCP server blocks (TENG-3921).
type Blocklist struct {
	Value       string `json:"blocklist"`
	Set         bool   `json:"set"`
	ManagedOnly bool   `json:"managed_devices_only"`
	Escrow      bool   `json:"device_escrow_enabled"`
}

// MCPServerBlocklist reads GET /v1/cli/mcp-servers/blocked, which accepts the
// CLI token. The dashboard route /v1/governance/settings accepts only a user
// JWT and refuses a CLI token with 401, so a machine could never read the list
// from there. It also carries the tenant's managed-devices-only and device
// escrow settings.
func (c *Client) MCPServerBlocklist(ctx context.Context) (Blocklist, error) {
	var resp Blocklist
	err := c.do(ctx, "GET", "/v1/cli/mcp-servers/blocked", nil, &resp)
	return resp, err
}

// EscrowEntry is one MCP entry this device took offline, credentials emptied.
type EscrowEntry struct {
	EscrowID     string   `json:"escrow_id,omitempty"`
	Installation string   `json:"installation_key,omitempty"`
	Server       string   `json:"server"`
	EntryKey     string   `json:"entry_key"`
	ClientID     string   `json:"client_id"`
	ItemsKey     string   `json:"items_key"`
	EntryJSON    string   `json:"entry_json"`
	StrippedKeys []string `json:"stripped_keys"`
}

// PutEscrow keeps one stripped entry in Telara.
func (c *Client) PutEscrow(ctx context.Context, e EscrowEntry) error {
	return c.do(ctx, "POST", "/v1/cli/estate-escrow", e, nil)
}

// ListEscrow returns this person's open entries for one device.
func (c *Client) ListEscrow(ctx context.Context, installationKey string) ([]EscrowEntry, error) {
	var resp struct {
		Entries []EscrowEntry `json:"entries"`
	}
	err := c.do(ctx, "GET", "/v1/cli/estate-escrow?installation_key="+url.QueryEscape(installationKey), nil, &resp)
	return resp.Entries, err
}

// RetireCollector retires this device's endpoint collector (TENG-3917), so an
// uninstalled laptop is not reported as silent. The next scan reinstates it.
func (c *Client) RetireCollector(ctx context.Context, installationKey string) (bool, error) {
	var resp struct {
		Retired bool `json:"retired"`
	}
	err := c.do(ctx, "POST", "/v1/cli/ai-estate/collector/retire", map[string]string{"installation_key": installationKey}, &resp)
	return resp.Retired, err
}

// MarkEscrowRestored closes an entry once it is back on the device.
func (c *Client) MarkEscrowRestored(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/cli/estate-escrow/"+url.PathEscape(id)+"/restored", map[string]string{}, nil)
}
