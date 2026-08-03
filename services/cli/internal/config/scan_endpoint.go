package config

// scanSubmitEndpoint is where `telara scan` submits discovery reports.
//
// It is a BUILD-TIME constant and is deliberately NOT resolvable from
// --api-url, TELARA_API_URL, or stored prefs, unlike every other CLI call.
//
// Why: a discovery report describes an employee's machine — which AI clients
// they run, which MCP servers they reach, which of those are unmediated. That
// evidence must land in the tenant's own estate and nowhere else. If the
// destination were a runtime flag, anyone able to set an env var could redirect
// a fleet's scan output to a host they control, or quietly point their own
// machine at a black hole to disappear from inventory. Neither should be
// possible, so the destination is compiled in.
//
// Local and development builds override it at link time, never at runtime:
//
//	go build -ldflags "-X gitlab.com/telara-labs/telara-cli/services/cli/internal/config.scanSubmitEndpoint=https://localhost:30443"
//
// A release build ships the production value below and ignores every runtime
// knob.
var scanSubmitEndpoint = "https://api.telara.dev"

// ScanSubmitEndpoint returns the pinned destination for discovery reports.
// Callers must use this rather than prefs.APIURL when submitting a scan.
func ScanSubmitEndpoint() string {
	return NormalizeAPIBaseURL(scanSubmitEndpoint)
}

// ScanEndpointIsOverridden reports whether this binary was linked against a
// non-production scan destination. `telara scan` prints a warning when true so
// a developer build can never be mistaken for one reporting to production.
func ScanEndpointIsOverridden() bool {
	return NormalizeAPIBaseURL(scanSubmitEndpoint) != NormalizeAPIBaseURL(defaultScanSubmitEndpoint)
}

// defaultScanSubmitEndpoint is the production destination, kept separate so the
// override check compares against a value the linker cannot rewrite.
const defaultScanSubmitEndpoint = "https://api.telara.dev"
