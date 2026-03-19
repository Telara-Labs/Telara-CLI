package api

// BugReportResponse is the response from the bug report endpoint.
type BugReportResponse struct {
	Success  bool   `json:"success"`
	ReportID string `json:"reportId"`
}
