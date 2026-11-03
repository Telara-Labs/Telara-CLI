package api

import (
	"context"
	"net/url"
	"time"
)

// HandoffRun is one accepted run the caller's own local runner serves.
type HandoffRun struct {
	ExecutionID    string    `json:"execution_id"`
	AssignmentID   string    `json:"assignment_id"`
	CatalogKey     string    `json:"catalog_key"`
	RuntimeID      string    `json:"runtime_id"`
	GrantExpiresAt time.Time `json:"grant_expires_at"`
	InputDelivered bool      `json:"input_delivered"`
}

// HandoffRunClaim is a grant-bound session for one run. Credential is returned once
// and must be held in memory only, for that run.
type HandoffRunClaim struct {
	Credential    string     `json:"credential"`
	IncarnationID string     `json:"incarnation_id"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ChildTaskID   string     `json:"child_task_id"`
	Run           HandoffRun `json:"run"`
}

// HandoffRunAttachment is what the runner saw after binding the session.
type HandoffRunAttachment struct {
	RuntimeVersion string `json:"runtime_version"`
	ThreadID       string `json:"thread_id"`
	AttachmentName string `json:"attachment_name"`
}

// HandoffRunInput is the approved first input: a brief and reference ids.
type HandoffRunInput struct {
	BriefJSON    string   `json:"brief_json"`
	ReferenceIDs []string `json:"reference_ids"`
	ChildTaskID  string   `json:"child_task_id"`
}

// Run statuses a runner reports.
const (
	HandoffRunRunning   = "HANDOFF_RUNNER_STATUS_RUNNING"
	HandoffRunCompleted = "HANDOFF_RUNNER_STATUS_COMPLETED"
	HandoffRunFailed    = "HANDOFF_RUNNER_STATUS_FAILED"
	HandoffRunCancelled = "HANDOFF_RUNNER_STATUS_CANCELLED"
)

// ListMyHandoffRuns lists the runs waiting for, or held by, the caller's runner.
func (c *Client) ListMyHandoffRuns(ctx context.Context, catalogKeys []string) ([]HandoffRun, error) {
	q := url.Values{}
	for _, key := range catalogKeys {
		q.Add("catalog_key", key)
	}
	path := "/v1/cli/handoff-runs"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var resp struct {
		Runs []HandoffRun `json:"runs"`
	}
	if err := c.do(ctx, "GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Runs, nil
}

// ClaimHandoffRun claims a session for one run. A second claim revokes the first.
// The claim proves the runner's key, and the session is bound to it (TENG-3693
// G05): every MCP call on it must carry a proof by the same key.
func (c *Client) ClaimHandoffRun(ctx context.Context, executionID string) (*HandoffRunClaim, error) {
	var resp HandoffRunClaim
	if err := c.doProved(ctx, "POST", "/v1/cli/handoff-runs/"+url.PathEscape(executionID)+"/claim", map[string]string{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetHandoffRunInput takes the approved first input once, for the claimed session.
func (c *Client) GetHandoffRunInput(ctx context.Context, executionID, incarnationID string, seen HandoffRunAttachment) (*HandoffRunInput, error) {
	var resp HandoffRunInput
	body := map[string]any{"incarnation_id": incarnationID, "attachment": seen}
	if err := c.doProved(ctx, "POST", "/v1/cli/handoff-runs/"+url.PathEscape(executionID)+"/input", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReportHandoffRun records the run's status. Stop says the run must end now.
func (c *Client) ReportHandoffRun(ctx context.Context, executionID, incarnationID, status string) (stop bool, recorded string, err error) {
	var resp struct {
		Stop     bool   `json:"stop"`
		Recorded string `json:"recorded"`
	}
	body := map[string]string{"incarnation_id": incarnationID, "status": status}
	if err := c.doProved(ctx, "POST", "/v1/cli/handoff-runs/"+url.PathEscape(executionID)+"/report", body, &resp); err != nil {
		return false, "", err
	}
	return resp.Stop, resp.Recorded, nil
}
