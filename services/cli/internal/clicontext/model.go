package clicontext

// Context holds a saved Telara context — a named combination of MCP config + API key.
type Context struct {
	Name         string `json:"name"`
	ConfigID     string `json:"config_id"`
	ConfigName   string `json:"config_name"`
	ScopeType    string `json:"scope_type"`    // tenant, team, project, user
	ScopeID      string `json:"scope_id"`
	APIKeyID     string `json:"api_key_id"`    // key ID used for revocation
	APIKeyPrefix string `json:"api_key_prefix"` // display only
	MCPURL       string `json:"mcp_url"`
	// RawKey is only populated at create time and is never persisted to disk.
	RawKey string `json:"raw_key,omitempty"`
}
