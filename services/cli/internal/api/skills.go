package api

import (
	"context"
	"net/url"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

// skills.go is the client for the shared-skill registry (TENG-1998).
//
// Distinct from the discovery routes in ai_estate.go on purpose: discovery is an
// unattended daily report that never carries a skill body, and sharing is an
// interactive, consent-gated upload that does. Sharing one client file would
// invite reusing the scan's scheduled path for an upload.

// ShareSkillRequest is the body of POST /v1/cli/skills.
type ShareSkillRequest = skillshare.ShareRequest

// ShareSkillResponse is returned after a successful share.
type ShareSkillResponse struct {
	SkillID string `json:"skill_id"`
	Version int32  `json:"version"`
	Scope   string `json:"scope"`
	// Superseded reports that this upload replaced an earlier version of the
	// same logical skill rather than creating a new one.
	Superseded bool `json:"superseded"`
}

// SharedSkill is one entry in the tenant's registry.
type SharedSkill struct {
	SkillID     string `json:"skill_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`
	Version     int32  `json:"version"`
	ContentHash string `json:"content_hash"`
	SharedBy    string `json:"shared_by,omitempty"`
	SharedAt    string `json:"shared_at,omitempty"`
	Revoked     bool   `json:"revoked"`
}

// ListSkillsResponse is the body of GET /v1/cli/skills.
type ListSkillsResponse struct {
	Skills []SharedSkill `json:"skills"`
}

// ShareSkill uploads a skill body to the tenant registry.
func (c *Client) ShareSkill(ctx context.Context, req ShareSkillRequest) (*ShareSkillResponse, error) {
	var resp ShareSkillResponse
	if err := c.do(ctx, "POST", "/v1/cli/skills", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListSharedSkills returns the skills visible to the caller.
func (c *Client) ListSharedSkills(ctx context.Context) (*ListSkillsResponse, error) {
	var resp ListSkillsResponse
	if err := c.do(ctx, "GET", "/v1/cli/skills", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RevokeSkill withdraws a shared skill.
//
// Revocation is real for team and enterprise scope: Telara serves the body, so
// removing it removes access. It is NOT real for open-source — that content may
// already be crawled, forked and indexed, and revoking only stops Telara serving
// it. The command layer says so explicitly rather than letting the API's success
// response imply a recall that did not happen.
func (c *Client) RevokeSkill(ctx context.Context, skillID string) error {
	return c.do(ctx, "DELETE", "/v1/cli/skills/"+url.PathEscape(skillID), nil, nil)
}
