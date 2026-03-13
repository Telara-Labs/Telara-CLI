package api

import (
	"context"
	"fmt"
)

// APIKey describes a single API key associated with an MCP configuration.
type APIKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"key_prefix"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	LastUsed  string `json:"last_used_at"`
	Revoked   bool   `json:"revoked"`
}

// GenerateKeyRequest is the request body for POST /v1/cli/configs/:id/keys.
type GenerateKeyRequest struct {
	Name             string `json:"name"`
	ScopeType        string `json:"scope_type"`        // e.g. "tenant"
	ScopeID          string `json:"scope_id"`           // empty for tenant-scoped
	ExpiresInSeconds int    `json:"expires_in_seconds"` // 0 = no expiry
}

// GenerateKeyResponse is the response body from generating a new API key.
// RawKey is only returned once at creation time.
type GenerateKeyResponse struct {
	KeyID  string `json:"id"`
	RawKey string `json:"raw_key"`
	Prefix string `json:"key_prefix"`
	MCPURL string `json:"mcp_url"`
}

// ListKeysResponse is the payload returned by GET /v1/cli/configs/:id/keys.
type ListKeysResponse struct {
	Keys []APIKey `json:"keys"`
}

// GenerateKey creates a new API key for the given MCP configuration ID.
func (c *Client) GenerateKey(ctx context.Context, configID string, req GenerateKeyRequest) (*GenerateKeyResponse, error) {
	var resp GenerateKeyResponse
	if err := c.do(ctx, "POST", "/v1/cli/configs/"+configID+"/keys", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListKeys returns all API keys for the given MCP configuration ID.
func (c *Client) ListKeys(ctx context.Context, configID string) (*ListKeysResponse, error) {
	var resp ListKeysResponse
	if err := c.do(ctx, "GET", "/v1/cli/configs/"+configID+"/keys", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RevokeKey revokes the API key with the given keyID, passing the owning configID
// as a query parameter as required by DELETE /v1/cli/keys/:key_id?config_id=:config_id.
func (c *Client) RevokeKey(ctx context.Context, keyID, configID string) error {
	path := fmt.Sprintf("/v1/cli/keys/%s?config_id=%s", keyID, configID)
	return c.do(ctx, "DELETE", path, nil, nil)
}
