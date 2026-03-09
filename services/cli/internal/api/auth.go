package api

import (
	"context"
)

// WhoamiResponse is the payload returned by the /v1/cli/auth/validate endpoint.
type WhoamiResponse struct {
	UserID      string   `json:"user_id"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	OrgName     string   `json:"org_name"`
	TenantID    string   `json:"tenant_id"`
	TokenPrefix string   `json:"token_prefix"`
	Scopes      []string `json:"scopes"`
}

// ValidateToken calls the API to verify the client's token and returns user details.
func (c *Client) ValidateToken(ctx context.Context) (*WhoamiResponse, error) {
	var resp WhoamiResponse
	if err := c.do(ctx, "POST", "/v1/cli/auth/validate", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RevokeToken instructs the API to revoke the client's current token.
func (c *Client) RevokeToken(ctx context.Context) error {
	return c.do(ctx, "DELETE", "/v1/cli/auth/token", nil, nil)
}
