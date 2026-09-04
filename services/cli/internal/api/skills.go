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
	// Risk is the SERVER's verdict, which is the enforcing one.
	//
	// The gateway has always returned this and the CLI has always dropped it on
	// the floor, so an author saw "shared" and never learned what was recorded
	// about their skill — including warn-level findings that did not block.
	Risk *RiskVerdict `json:"risk,omitempty"`
}

// RiskVerdict is the scan result recorded against a skill version.
//
// The FIRED RULES are the point. A score on its own is the opaque number the
// scanner design exists to avoid: an author cannot act on "47".
type RiskVerdict struct {
	Score         int32       `json:"score"`
	Threshold     int32       `json:"threshold"`
	Blocked       bool        `json:"blocked"`
	PolicyVersion string      `json:"policy_version,omitempty"`
	FiredRules    []FiredRule `json:"fired_rules,omitempty"`
}

// FiredRule is one rule's contribution to a verdict.
type FiredRule struct {
	RuleID      string `json:"rule_id"`
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	Occurrences int32  `json:"occurrences"`
	Points      int32  `json:"points"`
	Line        int32  `json:"line"`
	Excerpt     string `json:"excerpt,omitempty"`
	Explanation string `json:"explanation,omitempty"`
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
	// ApprovalState is why a shared skill may still not be loadable.
	//
	// The gateway returns it; without this field a skill PENDING review looked
	// identical to a live one in `telara skill list`, so an author had no way
	// to tell that nobody could actually use what they had shared.
	ApprovalState string `json:"approval_state,omitempty"`
	// The AUDIENCE (TENG-2717), distinct from Scope, which is reach.
	TargetScopeType string `json:"target_scope_type,omitempty"`
	TargetScopeID   string `json:"target_scope_id,omitempty"`
}

// SharedSkillDetail is one entry WITH its body, from GET /v1/cli/skills/{id}.
type SharedSkillDetail struct {
	SharedSkill
	Body string       `json:"body"`
	Risk *RiskVerdict `json:"risk,omitempty"`
	// StalePolicyVersion reports that the recorded assessment predates the
	// scanner rules running now. Surfaced rather than hidden: the reader should
	// know they are trusting an older judgement.
	StalePolicyVersion bool  `json:"stale_policy_version,omitempty"`
	AssetCount         int32 `json:"asset_count,omitempty"`
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

// GetSharedSkill fetches ONE skill with its body.
//
// This is the route index.json has advertised as each entry's "url" since the
// registry shipped, with no handler bound to it. Following that advertised URL
// is now the install path rather than a 404.
func (c *Client) GetSharedSkill(ctx context.Context, idOrName string) (*SharedSkillDetail, error) {
	var resp SharedSkillDetail
	if err := c.do(ctx, "GET", "/v1/cli/skills/"+url.PathEscape(idOrName), nil, &resp); err != nil {
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
