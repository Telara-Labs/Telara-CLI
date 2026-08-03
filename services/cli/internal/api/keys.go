package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// flexTime decodes a timestamp that the API may serialize as an RFC3339 string,
// a protobuf Timestamp object ({"seconds":N,"nanos":N} or {"Seconds":N,...}), a
// numeric unix epoch, or null. Prod returns MCP key timestamps as an object,
// which broke the previous `string`-typed fields (json: cannot unmarshal object
// into ... created_at of type string). Stored normalized to RFC3339 (or "").
type flexTime string

func (f *flexTime) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	switch b[0] {
	case '"': // JSON string — already a formatted timestamp (or empty)
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexTime(s)
		return nil
	case '{': // object — protobuf Timestamp {seconds,nanos}
		var obj struct {
			Seconds  json.Number `json:"seconds"`
			Nanos    json.Number `json:"nanos"`
			SecondsU json.Number `json:"Seconds"`
			NanosU   json.Number `json:"Nanos"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		secStr := string(obj.Seconds)
		if secStr == "" {
			secStr = string(obj.SecondsU)
		}
		nanoStr := string(obj.Nanos)
		if nanoStr == "" {
			nanoStr = string(obj.NanosU)
		}
		sec, _ := strconv.ParseInt(secStr, 10, 64)
		nanos, _ := strconv.ParseInt(nanoStr, 10, 64)
		if sec == 0 && nanos == 0 {
			*f = ""
			return nil
		}
		*f = flexTime(time.Unix(sec, nanos).UTC().Format(time.RFC3339))
		return nil
	default: // number — unix epoch seconds
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		sec, err := n.Int64()
		if err != nil {
			return err
		}
		*f = flexTime(time.Unix(sec, 0).UTC().Format(time.RFC3339))
		return nil
	}
}

// String renders the timestamp for display.
func (f flexTime) String() string { return string(f) }

// APIKey describes a single API key associated with an MCP configuration.
type APIKey struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Prefix    string   `json:"key_prefix"`
	ScopeType string   `json:"scope_type"`
	ScopeID   string   `json:"scope_id"`
	CreatedAt flexTime `json:"created_at"`
	ExpiresAt flexTime `json:"expires_at"`
	LastUsed  flexTime `json:"last_used_at"`
	Revoked   bool     `json:"revoked"`
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
