package api

import (
	"context"
	"net/url"
	"strconv"

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
	// ApprovalState is whether the skill is live or waiting for a tenant admin,
	// as the server decided from the audience. Rendered, never re-derived.
	ApprovalState string `json:"approval_state,omitempty"`
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

// ListDeniedSkills returns the tenant's active removals for this machine to
// enforce (TENG-2760).
func (c *Client) ListDeniedSkills(ctx context.Context) ([]skillshare.DeniedSkill, error) {
	var resp struct {
		Denied []skillshare.DeniedSkill `json:"denied"`
	}
	if err := c.do(ctx, "GET", "/v1/cli/skills/denied", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Denied, nil
}

// DenySkill records or lifts an admin's removal. Admin only, server-enforced.
func (c *Client) DenySkill(ctx context.Context, contentHash, skillName, reason string, lift bool) error {
	body := map[string]any{
		"contentHash": contentHash, "skillName": skillName,
		"reason": reason, "lift": lift,
	}
	return c.do(ctx, "POST", "/v1/cli/skills/denied", body, nil)
}

// SkillRequest is one ask in the queue (TENG-2761).
type SkillRequest struct {
	RequestID      string `json:"request_id"`
	SkillName      string `json:"skill_name"`
	ContentHash    string `json:"content_hash,omitempty"`
	Reason         string `json:"reason,omitempty"`
	RequestedBy    string `json:"requested_by"`
	RequestedAt    string `json:"requested_at,omitempty"`
	State          string `json:"state"`
	ResolvedBy     string `json:"resolved_by,omitempty"`
	ResolutionNote string `json:"resolution_note,omitempty"`
}

// RequestSkill asks the organisation for a skill: reinstate one that was
// removed, or promote one to tenant reach.
func (c *Client) RequestSkill(ctx context.Context, name, contentHash, reason string) (string, error) {
	var resp struct {
		RequestID string `json:"request_id"`
	}
	body := map[string]any{"skillName": name, "contentHash": contentHash, "reason": reason}
	if err := c.do(ctx, "POST", "/v1/cli/skills/requests", body, &resp); err != nil {
		return "", err
	}
	return resp.RequestID, nil
}

// ListSkillRequests returns the queue. Admins see every open request; anyone
// else sees only their own — the server decides which.
func (c *Client) ListSkillRequests(ctx context.Context, includeResolved bool) ([]SkillRequest, error) {
	path := "/v1/cli/skills/requests"
	if includeResolved {
		path += "?include_resolved=true"
	}
	var resp struct {
		Requests []SkillRequest `json:"requests"`
	}
	if err := c.do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Requests, nil
}

// PendingSkill is one entry in the promotion review queue.
type PendingSkill struct {
	SkillID           string `json:"skill_id"`
	Name              string `json:"name"`
	Scope             string `json:"scope"`
	Version           int    `json:"version"`
	SharedBy          string `json:"shared_by"`
	ContentHash       string `json:"content_hash"`
	ApprovalState     string `json:"approval_state"`
	ApprovalsRecorded int    `json:"approvals_recorded"`
	ApprovalsRequired int    `json:"approvals_required"`
}

// ListPendingSkills returns skills awaiting promotion review.
func (c *Client) ListPendingSkills(ctx context.Context) ([]PendingSkill, error) {
	var resp struct {
		Skills []PendingSkill `json:"skills"`
	}
	if err := c.do(ctx, "GET", "/v1/cli/skills/pending", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Skills, nil
}

// SkillApprovalResult is what one recorded decision changed.
type SkillApprovalResult struct {
	SkillID           string `json:"skill_id"`
	ApprovalsRecorded int    `json:"approvals_recorded"`
	ApprovalsRequired int    `json:"approvals_required"`
	State             string `json:"state"`
	Published         bool   `json:"published"`
}

// ApproveSkill records one promotion decision. Admin only, server-enforced.
//
// version is MANDATORY and is not derived here. Approval attaches to CONTENT,
// not to a name: if the author supersedes the skill between a reviewer reading
// it and deciding, an approval that named only the skill would land on bytes
// nobody reviewed. The server refuses version <= 0 for the same reason.
func (c *Client) ApproveSkill(ctx context.Context, skillID string, version int, approve bool, note string) (*SkillApprovalResult, error) {
	body := map[string]any{"version": version, "approve": approve, "note": note}
	var resp SkillApprovalResult
	if err := c.do(ctx, "POST", "/v1/cli/skills/"+url.PathEscape(skillID)+"/approve", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SkillAdoption is one cluster of identical skill bytes across the fleet.
//
// PrincipalCount is a POINTER because zero and unknown are different answers.
// The estate only began recording a principal token recently, so a cluster made
// of older rows can be counted per machine but not per person; nil renders as
// unknown, and a real 0 could only mean "nobody", which would be a lie.
type SkillAdoption struct {
	ContentHash         string `json:"content_hash"`
	SkillName           string `json:"skill_name"`
	Description         string `json:"description,omitempty"`
	InstallCount        int    `json:"install_count"`
	PrincipalCount      *int   `json:"principal_count,omitempty"`
	NameVariantCount    int    `json:"name_variant_count,omitempty"`
	HasExecutable       bool   `json:"has_executable,omitempty"`
	AlreadyShared       bool   `json:"already_shared,omitempty"`
	SharedSkillID       string `json:"shared_skill_id,omitempty"`
	SharedApprovalState string `json:"shared_approval_state,omitempty"`
	Denied              bool   `json:"denied,omitempty"`
	FirstSeen           string `json:"first_seen,omitempty"`
	LastSeen            string `json:"last_seen,omitempty"`
}

// SkillAdoptionReport carries the clusters together with the denominator they
// have to be read against.
type SkillAdoptionReport struct {
	Skills                []SkillAdoption `json:"skills"`
	TotalDevicesReporting int             `json:"total_devices_reporting"`
	MinInstallsApplied    int             `json:"min_installs_applied"`
}

// ListSkillAdoption reports which skills are already spreading on their own.
// Admin only, server-enforced.
func (c *Client) ListSkillAdoption(ctx context.Context, minInstalls int, includeShared bool) (*SkillAdoptionReport, error) {
	q := url.Values{}
	if minInstalls > 0 {
		q.Set("min_installs", strconv.Itoa(minInstalls))
	}
	if includeShared {
		q.Set("include_shared", "true")
	}
	path := "/v1/cli/skills/adoption"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp SkillAdoptionReport
	if err := c.do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveSkillRequest grants or declines one. Admin only, server-enforced.
func (c *Client) ResolveSkillRequest(ctx context.Context, requestID string, grant bool, note string) error {
	body := map[string]any{"grant": grant, "note": note}
	return c.do(ctx, "POST", "/v1/cli/skills/requests/"+url.PathEscape(requestID)+"/resolve", body, nil)
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
