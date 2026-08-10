package discovery

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var fixedStarted = time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
var fixedCompleted = time.Date(2026, 7, 29, 15, 0, 5, 123456789, time.UTC)

func testCollector() CollectorIdentity {
	return CollectorIdentity{
		CollectorKind:     "cli",
		CollectorVersion:  "0.0.0-test",
		InstallationKey:   "install-test-key",
		NonHumanPrincipal: false,
	}
}

func TestWireCollectionStatusAndAuthorizesTombstone(t *testing.T) {
	tests := []struct {
		name           string
		status         ScanStatus
		wantCollection string
		wantTombstone  bool
	}{
		{
			name:           "ScanOK is COMPLETE and authorizes tombstone",
			status:         ScanOK,
			wantCollection: WireCollectionComplete,
			wantTombstone:  true,
		},
		{
			name:           "ScanFileAbsent is COMPLETE and authorizes tombstone",
			status:         ScanFileAbsent,
			wantCollection: WireCollectionComplete,
			wantTombstone:  true,
		},
		{
			name:           "ScanParseFailed is FAILED and does not authorize tombstone",
			status:         ScanParseFailed,
			wantCollection: WireCollectionFailed,
			wantTombstone:  false,
		},
		{
			name:           "ScanPermissionDenied is FAILED and does not authorize tombstone",
			status:         ScanPermissionDenied,
			wantCollection: WireCollectionFailed,
			wantTombstone:  false,
		},
		{
			name:           "ScanUnsupported is PARTIAL and does not authorize tombstone",
			status:         ScanUnsupported,
			wantCollection: WireCollectionPartial,
			wantTombstone:  false,
		},
		{
			name:           "unknown ScanStatus is FAILED and does not authorize tombstone",
			status:         ScanStatus("ScanTotallyBogus"),
			wantCollection: WireCollectionFailed,
			wantTombstone:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wireCollectionStatus(tt.status)
			if got != tt.wantCollection {
				t.Fatalf("wireCollectionStatus(%q) = %q, want %q", tt.status, got, tt.wantCollection)
			}
			if AuthorizesTombstone(tt.status) != tt.wantTombstone {
				t.Fatalf("AuthorizesTombstone(%q) = %v, want %v", tt.status, AuthorizesTombstone(tt.status), tt.wantTombstone)
			}
		})
	}
}

func TestBuildReportMixedScanCoverageHonesty(t *testing.T) {
	// One OK scope with servers, one parse-failed (with garbage servers that must
	// NOT become assertions), one absent, one unsupported.
	results := []ConfigScanResult{
		{
			ClientFamily: "cursor",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{
					ServerName:      "github",
					Transport:       TransportStdio,
					CommandIdentity: "npx:-y:@modelcontextprotocol/server-github",
					RawArgCount:     2,
					CredentialClass: CredentialNone,
				},
				{
					ServerName:      "remote",
					Transport:       TransportHTTP,
					EndpointHost:    "https://mcp.example.com",
					CredentialClass: CredentialInline,
					CredentialHint:  "Authorization",
				},
			},
		},
		{
			ClientFamily: "claude",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanParseFailed,
			ErrorClass:   "json_parse",
			// Adversarial: servers attached to a failed scan must be ignored.
			Servers: []DiscoveredServer{
				{
					ServerName:      "phantom",
					Transport:       TransportStdio,
					CredentialClass: CredentialInline,
					CredentialHint:  "Authorization",
				},
			},
		},
		{
			ClientFamily: "gemini",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanFileAbsent,
		},
		{
			ClientFamily: "vscode",
			Scope:        "global",
			PathClass:    "managed_system",
			Status:       ScanUnsupported,
			ErrorClass:   "scope_unsupported",
		},
	}

	report := BuildReport(testCollector(), "scan-mixed-1", fixedStarted, fixedCompleted, results)

	if len(report.Scopes) != 4 {
		t.Fatalf("expected 4 scopes (coverage honesty), got %d", len(report.Scopes))
	}

	byScope := map[string]CollectionScope{}
	for _, s := range report.Scopes {
		byScope[s.SourceScope] = s
	}
	wantStatuses := map[string]string{
		"cursor:global": WireCollectionComplete,
		"claude:global": WireCollectionFailed,
		"gemini:global": WireCollectionComplete, // FileAbsent is COMPLETE
		"vscode:global": WireCollectionPartial,
	}
	for scope, want := range wantStatuses {
		got, ok := byScope[scope]
		if !ok {
			t.Fatalf("missing scope %q — omission is indistinguishable from success", scope)
		}
		if got.CollectionStatus != want {
			t.Fatalf("scope %q status = %q, want %q", scope, got.CollectionStatus, want)
		}
	}

	// Assertions ONLY from the OK scope.
	if len(report.ResourceAssertions) != 2 {
		t.Fatalf("expected 2 assertions from OK scope only, got %d: %+v", len(report.ResourceAssertions), report.ResourceAssertions)
	}
	for _, a := range report.ResourceAssertions {
		if a.SourceScope != "cursor:global" {
			t.Fatalf("assertion leaked from non-OK scope %q: %+v", a.SourceScope, a)
		}
		if a.ServerName == "phantom" {
			t.Fatalf("parse-failed phantom server was promoted to an assertion")
		}
	}

	// FileAbsent authorizes tombstone; ParseFailed does not.
	if !AuthorizesTombstone(ScanFileAbsent) {
		t.Fatal("ScanFileAbsent must authorize tombstone")
	}
	if AuthorizesTombstone(ScanParseFailed) {
		t.Fatal("ScanParseFailed must NOT authorize tombstone")
	}
}

func TestBuildReportInstanceSeparationSameServerTwoScopes(t *testing.T) {
	results := []ConfigScanResult{
		{
			ClientFamily: "cursor",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{ServerName: "shared-mcp", Transport: TransportStdio, CredentialClass: CredentialNone},
			},
		},
		{
			ClientFamily: "claude",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{ServerName: "shared-mcp", Transport: TransportStdio, CredentialClass: CredentialNone},
			},
		},
	}

	report := BuildReport(testCollector(), "scan-instance-1", fixedStarted, fixedCompleted, results)

	if len(report.ResourceAssertions) != 2 {
		t.Fatalf("expected 2 distinct assertions, got %d", len(report.ResourceAssertions))
	}

	ids := map[string]struct{}{}
	scopes := map[string]struct{}{}
	for _, a := range report.ResourceAssertions {
		if a.ServerName != "shared-mcp" {
			t.Fatalf("unexpected server name %q", a.ServerName)
		}
		ids[a.SourceAssertionID] = struct{}{}
		scopes[a.SourceScope] = struct{}{}
		wantID := a.SourceScope + "|shared-mcp"
		if a.SourceAssertionID != wantID {
			t.Fatalf("SourceAssertionID = %q, want %q", a.SourceAssertionID, wantID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("assertion IDs collapsed: got %v", ids)
	}
	if len(scopes) != 2 {
		t.Fatalf("scopes collapsed: got %v", scopes)
	}
	if _, ok := ids["cursor:global|shared-mcp"]; !ok {
		t.Fatal("missing cursor:global|shared-mcp assertion id")
	}
	if _, ok := ids["claude:global|shared-mcp"]; !ok {
		t.Fatal("missing claude:global|shared-mcp assertion id")
	}
}

func TestBuildReportDeterministicUnderInputReorder(t *testing.T) {
	base := []ConfigScanResult{
		{
			ClientFamily: "cursor",
			Scope:        "project",
			PathClass:    "project_local",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{ServerName: "zeta", Transport: TransportStdio, CredentialClass: CredentialNone},
				{ServerName: "alpha", Transport: TransportHTTP, EndpointHost: "https://a.example", CredentialClass: CredentialEnvReferenced, CredentialHint: "API_KEY"},
			},
		},
		{
			ClientFamily: "claude",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanParseFailed,
			ErrorClass:   "json_parse",
		},
		{
			ClientFamily: "gemini",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanFileAbsent,
		},
		{
			ClientFamily: "cursor",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{ServerName: "mid", Transport: TransportSSE, EndpointHost: "https://m.example", CredentialClass: CredentialOAuthManaged},
			},
		},
	}

	reordered := []ConfigScanResult{
		base[3],
		base[1],
		base[0],
		base[2],
	}

	a := BuildReport(testCollector(), "scan-det-1", fixedStarted, fixedCompleted, base)
	b := BuildReport(testCollector(), "scan-det-1", fixedStarted, fixedCompleted, reordered)

	if !reflect.DeepEqual(a, b) {
		t.Fatalf("reports differ under input reorder:\n  a=%+v\n  b=%+v", a, b)
	}

	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if !bytes.Equal(ja, jb) {
		t.Fatalf("marshalled JSON differs under input reorder:\n  a=%s\n  b=%s", ja, jb)
	}

	// Scopes and assertions must be sorted by their identity keys.
	for i := 1; i < len(a.Scopes); i++ {
		if a.Scopes[i-1].SourceScope >= a.Scopes[i].SourceScope {
			t.Fatalf("scopes not sorted: %q then %q", a.Scopes[i-1].SourceScope, a.Scopes[i].SourceScope)
		}
	}
	for i := 1; i < len(a.ResourceAssertions); i++ {
		if a.ResourceAssertions[i-1].SourceAssertionID >= a.ResourceAssertions[i].SourceAssertionID {
			t.Fatalf("assertions not sorted: %q then %q",
				a.ResourceAssertions[i-1].SourceAssertionID,
				a.ResourceAssertions[i].SourceAssertionID)
		}
	}
}

func TestWireCredentialClassTotality(t *testing.T) {
	tests := []struct {
		name  string
		class CredentialClass
		want  string
	}{
		{name: "none", class: CredentialNone, want: WireCredentialNone},
		{name: "inline", class: CredentialInline, want: WireCredentialInline},
		{name: "env", class: CredentialEnvReferenced, want: WireCredentialEnvReferenced},
		{name: "oauth", class: CredentialOAuthManaged, want: WireCredentialOAuthManaged},
		{name: "unknown", class: CredentialUnknown, want: WireCredentialUnknown},
		{name: "zero_value", class: CredentialClass(""), want: WireCredentialUnknown},
		{name: "unrecognised", class: CredentialClass("CredentialTotallyNew"), want: WireCredentialUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wireCredentialClass(tt.class)
			if got != tt.want {
				t.Fatalf("wireCredentialClass(%q) = %q, want %q", tt.class, got, tt.want)
			}
			if got == WireCredentialNone && tt.class != CredentialNone {
				t.Fatalf("unrecognised class %q must never map to NONE (understates exposure)", tt.class)
			}
		})
	}

	// End-to-end: zero-value credential on an OK server becomes UNKNOWN in the report.
	report := BuildReport(testCollector(), "scan-cred-1", fixedStarted, fixedCompleted, []ConfigScanResult{
		{
			ClientFamily: "cursor",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{ServerName: "mystery", Transport: TransportUnknown, CredentialClass: CredentialClass("")},
			},
		},
	})
	if len(report.ResourceAssertions) != 1 {
		t.Fatalf("expected 1 assertion, got %d", len(report.ResourceAssertions))
	}
	if report.ResourceAssertions[0].CredentialClass != WireCredentialUnknown {
		t.Fatalf("zero CredentialClass mapped to %q, want %q (not NONE)",
			report.ResourceAssertions[0].CredentialClass, WireCredentialUnknown)
	}
	if report.ResourceAssertions[0].CredentialClass == WireCredentialNone {
		t.Fatal("zero CredentialClass must not become NONE")
	}
}

func TestBuildReportJSONRoundTripNoSecretLeak(t *testing.T) {
	const secretValue = "sk-live-SHOULD-NEVER-APPEAR"
	results := []ConfigScanResult{
		{
			ClientFamily: "cursor",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanOK,
			Servers: []DiscoveredServer{
				{
					ServerName:      "secretful",
					Transport:       TransportHTTP,
					EndpointHost:    "https://api.example.com",
					CredentialClass: CredentialInline,
					CredentialHint:  "Authorization",
					// Credential VALUE is intentionally not a field on DiscoveredServer;
					// we still assert the report JSON never contains a planted secret
					// string and only carries the key hint.
				},
			},
		},
		{
			ClientFamily: "claude",
			Scope:        "global",
			PathClass:    "user_home_dotfile",
			Status:       ScanPermissionDenied,
			ErrorClass:   "permission_denied",
		},
	}

	report := BuildReport(testCollector(), "scan-json-1", fixedStarted, fixedCompleted, results)

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("report JSON leaked credential value %q: %s", secretValue, raw)
	}

	var round DiscoveryReport
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(report, round) {
		t.Fatalf("round-trip mismatch:\n  orig=%+v\n  round=%+v", report, round)
	}

	// Timestamps must be RFC3339-parseable (BuildReport uses RFC3339Nano).
	for _, ts := range []string{report.CollectionStartedAt, report.CollectionCompletedAt} {
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Fatalf("timestamp %q is not RFC3339: %v", ts, err)
		}
	}
	for _, a := range report.ResourceAssertions {
		if _, err := time.Parse(time.RFC3339Nano, a.ObservedAt); err != nil {
			t.Fatalf("ObservedAt %q is not RFC3339: %v", a.ObservedAt, err)
		}
		if a.CredentialKeyHint != "Authorization" {
			t.Fatalf("CredentialKeyHint = %q, want Authorization (key name only)", a.CredentialKeyHint)
		}
		if a.CredentialClass != WireCredentialInline {
			t.Fatalf("CredentialClass = %q, want INLINE", a.CredentialClass)
		}
	}

	// Adversarial field-leak check: raw JSON must not invent unexpected top-level keys.
	var loose map[string]json.RawMessage
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatalf("loose unmarshal: %v", err)
	}
	allowedTop := map[string]struct{}{
		"schemaVersion":         {},
		"collector":             {},
		"scanId":                {},
		"collectionStartedAt":   {},
		"collectionCompletedAt": {},
		"scopes":                {},
		"resourceAssertions":    {},
	}
	for k := range loose {
		if _, ok := allowedTop[k]; !ok {
			t.Fatalf("unexpected top-level field leaked in report JSON: %q", k)
		}
	}

	var assertions []map[string]json.RawMessage
	if err := json.Unmarshal(loose["resourceAssertions"], &assertions); err != nil {
		t.Fatalf("assertions unmarshal: %v", err)
	}
	if len(assertions) != 1 {
		t.Fatalf("expected 1 assertion in JSON, got %d", len(assertions))
	}
	allowedAssertion := map[string]struct{}{
		"sourceScope":       {},
		"sourceAssertionId": {},
		"providerNamespace": {},
		"resourceKind":      {},
		"serverName":        {},
		"transport":         {},
		"endpointHost":      {},
		"commandIdentity":   {},
		"argCount":          {},
		"credentialClass":   {},
		"credentialKeyHint": {},
		// Discovery claim about mediation: DISCOVERED_UNMEDIATED or
		// DISCOVERED_MANAGED. Carries no credential material.
		"controlState": {},
		"observedAt":   {},
	}
	for k := range assertions[0] {
		if _, ok := allowedAssertion[k]; !ok {
			t.Fatalf("unexpected assertion field leaked: %q", k)
		}
		if strings.Contains(strings.ToLower(k), "secret") || strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "password") {
			t.Fatalf("assertion field name looks like a secret carrier: %q", k)
		}
	}
}

func TestSummarizeAllFailedReport(t *testing.T) {
	results := []ConfigScanResult{
		{ClientFamily: "cursor", Scope: "global", PathClass: "user_home_dotfile", Status: ScanParseFailed, ErrorClass: "json_parse"},
		{ClientFamily: "claude", Scope: "global", PathClass: "user_home_dotfile", Status: ScanPermissionDenied, ErrorClass: "permission_denied"},
		{ClientFamily: "gemini", Scope: "global", PathClass: "user_home_dotfile", Status: ScanParseFailed, ErrorClass: "json_parse"},
	}

	report := BuildReport(testCollector(), "scan-fail-all", fixedStarted, fixedCompleted, results)

	if len(report.Scopes) != 3 {
		t.Fatalf("expected 3 scopes, got %d", len(report.Scopes))
	}
	if len(report.ResourceAssertions) != 0 {
		t.Fatalf("all-failed scan must produce zero assertions, got %d", len(report.ResourceAssertions))
	}

	summary := Summarize(report)
	if summary.ScopesTotal != 3 {
		t.Fatalf("ScopesTotal = %d, want 3", summary.ScopesTotal)
	}
	if summary.ScopesComplete != 0 {
		t.Fatalf("ScopesComplete = %d, want 0 (must not look like perfect coverage)", summary.ScopesComplete)
	}
	if summary.ScopesPartial != 0 {
		t.Fatalf("ScopesPartial = %d, want 0", summary.ScopesPartial)
	}
	if summary.ScopesFailed != 3 {
		t.Fatalf("ScopesFailed = %d, want 3", summary.ScopesFailed)
	}
}

func TestFailedScopeNeverEmitsAssertionsEvenWithServers(t *testing.T) {
	statuses := []ScanStatus{ScanParseFailed, ScanPermissionDenied, ScanFileAbsent, ScanUnsupported}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			report := BuildReport(testCollector(), "scan-no-assert", fixedStarted, fixedCompleted, []ConfigScanResult{
				{
					ClientFamily: "cursor",
					Scope:        "global",
					PathClass:    "user_home_dotfile",
					Status:       status,
					Servers: []DiscoveredServer{
						{ServerName: "should-not-appear", Transport: TransportStdio, CredentialClass: CredentialNone},
					},
				},
			})
			if len(report.Scopes) != 1 {
				t.Fatalf("expected 1 scope, got %d", len(report.Scopes))
			}
			if len(report.ResourceAssertions) != 0 {
				t.Fatalf("status %q must emit no assertions, got %+v", status, report.ResourceAssertions)
			}
		})
	}
}

// TestPathClassProjectUnderHomeIsNotUserGlobal is a regression test.
//
// Developers keep their projects inside their home directory, so classifying
// "anything under $HOME" as user_global mislabelled essentially every
// project-scoped config. Scope is what authorises a tombstone, so a wrong
// scope label lets one scope's complete scan retire another scope's assets.
func TestPathClassProjectUnderHomeIsNotUserGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectConfig := filepath.Join(home, "Desktop", "Projects", "acme", ".cursor", "mcp.json")
	if got := PathClass(projectConfig); got != "project_local" {
		t.Fatalf("project config under home classified as %q, want project_local", got)
	}

	// The genuine global location must still classify as global.
	globalConfig := filepath.Join(home, ".cursor", "mcp.json")
	if got := PathClass(globalConfig); got != "user_global:cursor" {
		t.Fatalf("global cursor config classified as %q, want user_global:cursor", got)
	}

	// And no classification may ever echo the username or an absolute path.
	for _, p := range []string{projectConfig, globalConfig} {
		class := PathClass(p)
		if strings.Contains(class, home) || strings.Contains(class, string(filepath.Separator)+"Users") {
			t.Fatalf("PathClass(%q) leaked a filesystem path: %q", p, class)
		}
	}
}

// TestApplyManagedEndpointsClassifiesByHostNotName guards the control-state
// classification. An unused direct MCP must read as DISCOVERED_UNMEDIATED — if
// it silently defaulted to UNKNOWN, the exposure this feature exists to surface
// would be invisible, which is exactly how the full-stack E2E first failed.
func TestApplyManagedEndpointsClassifiesByHostNotName(t *testing.T) {
	results := []ConfigScanResult{{
		ClientFamily: "claude-code", Scope: "global", PathClass: "user_global:claude_code",
		Status: ScanOK,
		Servers: []DiscoveredServer{
			{ServerName: "telara", Transport: TransportHTTP, EndpointHost: "https://api.telara.dev", CredentialClass: CredentialInline, CredentialHint: "Authorization"},
			// Deliberately named "telara" but pointing somewhere else. Name is
			// not evidence; only the host decides.
			{ServerName: "telara", Transport: TransportHTTP, EndpointHost: "https://evil.example.com", CredentialClass: CredentialNone},
			{ServerName: "local-tool", Transport: TransportStdio, CommandIdentity: "npx:-y:@foo/mcp", CredentialClass: CredentialNone},
		},
	}}

	report := BuildReport(testCollector(), "scan-ctl-1", fixedStarted, fixedCompleted, results)

	for _, a := range report.ResourceAssertions {
		if a.ControlState != WireControlDiscoveredUnmediated {
			t.Fatalf("before classification %q should be unmediated, got %q", a.ServerName, a.ControlState)
		}
	}

	ApplyManagedEndpoints(&report, []string{"https://api.telara.dev"})

	got := map[string]string{}
	for _, a := range report.ResourceAssertions {
		key := a.ServerName + "|" + a.EndpointHost + "|" + a.CommandIdentity
		got[key] = a.ControlState
	}

	if s := got["telara|https://api.telara.dev|"]; s != WireControlDiscoveredManaged {
		t.Errorf("telara endpoint should be managed, got %q", s)
	}
	if s := got["telara|https://evil.example.com|"]; s != WireControlDiscoveredUnmediated {
		t.Errorf("impostor named 'telara' must stay unmediated (name is not evidence), got %q", s)
	}
	if s := got["local-tool||npx:-y:@foo/mcp"]; s != WireControlDiscoveredUnmediated {
		t.Errorf("stdio server can never be gateway-managed, got %q", s)
	}
}
