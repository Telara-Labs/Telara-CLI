package api

import "context"

// MCPConfig represents a single MCP configuration returned by the API.
type MCPConfig struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ScopeType   string `json:"scope_type"`
	ScopeID     string `json:"scope_id"`
	DataSources int    `json:"data_source_count"`
	Status      string `json:"status"`
	MCPURL      string `json:"mcp_url"`
}

// ListConfigsResponse is the payload returned by GET /v1/cli/configs.
type ListConfigsResponse struct {
	Configs []MCPConfig `json:"configs"`
}

// ConfigDetail extends MCPConfig with per-configuration detail fields.
type ConfigDetail struct {
	MCPConfig
	DataSources []DataSource `json:"data_sources"`
	PolicyCount int          `json:"policy_count"`
	KeyCount    int          `json:"key_count"`
}

// DataSource describes a single data source attached to an MCP configuration.
type DataSource struct {
	Name        string `json:"name"`
	Integration string `json:"integration"`
}

// ResolveResponse is the payload returned by GET /v1/cli/configs/resolve.
type ResolveResponse struct {
	Managed []MCPConfig `json:"managed"`
	User    []MCPConfig `json:"user"`
	Project []MCPConfig `json:"project"`
}

// ListConfigs fetches all MCP configurations accessible to the authenticated user.
func (c *Client) ListConfigs(ctx context.Context) (*ListConfigsResponse, error) {
	var resp ListConfigsResponse
	if err := c.do(ctx, "GET", "/v1/cli/configs", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetConfig fetches the detail of a single MCP configuration by name or ID.
func (c *Client) GetConfig(ctx context.Context, idOrName string) (*ConfigDetail, error) {
	var resp ConfigDetail
	if err := c.do(ctx, "GET", "/v1/cli/configs/"+idOrName, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveConfigs returns the set of MCP configurations resolved for the current user,
// split into managed, user-scoped, and project-scoped buckets.
func (c *Client) ResolveConfigs(ctx context.Context) (*ResolveResponse, error) {
	var resp ResolveResponse
	if err := c.do(ctx, "GET", "/v1/cli/configs/resolve", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
