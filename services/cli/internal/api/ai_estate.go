package api

import (
	"context"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// DiscoveryReportRequest is the JSON body for POST /v1/cli/ai-estate/discovery-reports.
// It matches discovery.DiscoveryReport wire shape.
type DiscoveryReportRequest = discovery.DiscoveryReport

// SubmitDiscoveryReportResponse is returned after a successful ingest.
type SubmitDiscoveryReportResponse struct {
	CollectionRunID        string   `json:"collection_run_id"`
	Duplicate              bool     `json:"duplicate"`
	AcceptedAssertionCount int32    `json:"accepted_assertion_count"`
	RejectedAssertionCount int32    `json:"rejected_assertion_count"`
	ScopeCount             int32    `json:"scope_count"`
	RejectionClasses       []string `json:"rejection_classes"`
}

// SubmitDiscoveryReport posts a discovery report to the Gateway CLI ingest route.
func (c *Client) SubmitDiscoveryReport(ctx context.Context, report DiscoveryReportRequest) (*SubmitDiscoveryReportResponse, error) {
	var resp SubmitDiscoveryReportResponse
	if err := c.do(ctx, "POST", "/v1/cli/ai-estate/discovery-reports", report, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
