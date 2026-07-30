package discovery

import (
	"fmt"
	"sort"
	"time"
)

// ReportSchemaVersion identifies the wire shape below. The gateway converts a
// report into the AIEstateDiscoveryReport protobuf; this package deliberately
// does NOT depend on telara-proto so the CLI stays a small, dependency-light
// binary that ships to employee machines.
const ReportSchemaVersion = "ai-estate-discovery-report/v1"

// Credential class values on the wire. These mirror proto
// AIEstateCredentialClass by name so the gateway conversion is a total mapping
// with no defaulting. An unrecognised value must be rejected, never coerced to
// NONE — "unknown" and "absent" are different facts.
const (
	WireCredentialUnspecified   = "AI_ESTATE_CREDENTIAL_CLASS_UNSPECIFIED"
	WireCredentialNone          = "AI_ESTATE_CREDENTIAL_CLASS_NONE"
	WireCredentialInline        = "AI_ESTATE_CREDENTIAL_CLASS_INLINE"
	WireCredentialEnvReferenced = "AI_ESTATE_CREDENTIAL_CLASS_ENV_REFERENCED"
	WireCredentialOAuthManaged  = "AI_ESTATE_CREDENTIAL_CLASS_OAUTH_MANAGED"
	WireCredentialUnknown       = "AI_ESTATE_CREDENTIAL_CLASS_UNKNOWN"
)

// Collection status values, mirroring proto AIEstateCollectionStatus. Only
// COMPLETE may ever license a tombstone downstream.
const (
	WireCollectionComplete = "AI_ESTATE_COLLECTION_STATUS_COMPLETE"
	WireCollectionPartial  = "AI_ESTATE_COLLECTION_STATUS_PARTIAL"
	WireCollectionFailed   = "AI_ESTATE_COLLECTION_STATUS_FAILED"
)

// Control state values, mirroring proto AIEstateControlState.
//
// A discovery report may only make a DISCOVERY claim about control. Finding a
// configuration on disk establishes that it exists and where it points; it says
// nothing about whether any call through it was actually mediated. So the
// collector emits DISCOVERED_UNMEDIATED or DISCOVERED_MANAGED and never
// MEDIATED_ENFORCED — that is a runtime claim backed by an observed call, and
// only the gateway can make it.
const (
	WireControlDiscoveredUnmediated = "AI_ESTATE_CONTROL_STATE_DISCOVERED_UNMEDIATED"
	WireControlDiscoveredManaged    = "AI_ESTATE_CONTROL_STATE_DISCOVERED_MANAGED"
)

// CollectorIdentity describes the reporting collector. The gateway overrides
// ReportedPrincipalID with the authenticated CLI token's user; a collector
// cannot assert evidence on behalf of someone else without explicit delegation.
type CollectorIdentity struct {
	CollectorKind       string `json:"collectorKind"`
	CollectorVersion    string `json:"collectorVersion"`
	InstallationKey     string `json:"installationKey"`
	ReportedPrincipalID string `json:"reportedPrincipalId,omitempty"`
	DeviceLabelClass    string `json:"deviceLabelClass,omitempty"`
	NonHumanPrincipal   bool   `json:"nonHumanPrincipal"`
}

// CollectionScope reports one (client, scope) boundary that the scan ATTEMPTED,
// including boundaries that turned out to be absent, unreadable or unsupported.
type CollectionScope struct {
	SourceScope       string `json:"sourceScope"`
	ProviderNamespace string `json:"providerNamespace"`
	PathClass         string `json:"pathClass,omitempty"`
	CollectionStatus  string `json:"collectionStatus"`
	ErrorClass        string `json:"errorClass,omitempty"`
}

// ResourceAssertion is one claim about one discovered MCP server, scoped to the
// configuration instance that produced it. The per-employee configuration
// instance and the deduplicated logical server are both required by the product
// contract, so the assertion carries the instance-level scope rather than
// collapsing to a bare server identity.
type ResourceAssertion struct {
	SourceScope       string `json:"sourceScope"`
	SourceAssertionID string `json:"sourceAssertionId"`
	ProviderNamespace string `json:"providerNamespace"`
	ResourceKind      string `json:"resourceKind"`
	ServerName        string `json:"serverName"`
	Transport         string `json:"transport"`
	EndpointHost      string `json:"endpointHost,omitempty"`
	CommandIdentity   string `json:"commandIdentity,omitempty"`
	ArgCount          int    `json:"argCount"`
	CredentialClass   string `json:"credentialClass"`
	CredentialKeyHint string `json:"credentialKeyHint,omitempty"`
	ControlState      string `json:"controlState"`
	ObservedAt        string `json:"observedAt"`
}

// DiscoveryReport is the unit the collector submits. It is idempotent on
// (tenant, collector, scanId).
type DiscoveryReport struct {
	SchemaVersion         string              `json:"schemaVersion"`
	Collector             CollectorIdentity   `json:"collector"`
	ScanID                string              `json:"scanId"`
	CollectionStartedAt   string              `json:"collectionStartedAt"`
	CollectionCompletedAt string              `json:"collectionCompletedAt"`
	Scopes                []CollectionScope   `json:"scopes"`
	ResourceAssertions    []ResourceAssertion `json:"resourceAssertions"`
}

// wireCredentialClass maps a scanner classification onto its wire name. It is
// total by construction: an unhandled class becomes UNKNOWN rather than NONE,
// because reporting "no credential" for something we failed to classify would
// understate exposure.
func wireCredentialClass(c CredentialClass) string {
	switch c {
	case CredentialNone:
		return WireCredentialNone
	case CredentialInline:
		return WireCredentialInline
	case CredentialEnvReferenced:
		return WireCredentialEnvReferenced
	case CredentialOAuthManaged:
		return WireCredentialOAuthManaged
	case CredentialUnknown:
		return WireCredentialUnknown
	default:
		return WireCredentialUnknown
	}
}

// wireCollectionStatus maps a scan outcome onto its wire name.
//
// Only ScanOK is COMPLETE. ScanFileAbsent is also COMPLETE and that is
// deliberate: we successfully determined the file is not there, which is a
// real, complete answer for that scope and may legitimately retire a
// previously-seen configuration. ScanParseFailed and ScanPermissionDenied are
// FAILED — we learned nothing, and must not be allowed to retire anything.
// ScanUnsupported is PARTIAL: the scope exists but we cannot cover it.
func wireCollectionStatus(s ScanStatus) string {
	switch s {
	case ScanOK, ScanFileAbsent:
		return WireCollectionComplete
	case ScanParseFailed, ScanPermissionDenied:
		return WireCollectionFailed
	case ScanUnsupported:
		return WireCollectionPartial
	default:
		return WireCollectionFailed
	}
}

// AuthorizesTombstone reports whether a scope's status may retire assets that
// were previously seen in that scope and are absent now. Exposed so callers
// cannot re-derive this rule slightly differently somewhere else.
func AuthorizesTombstone(s ScanStatus) bool {
	return wireCollectionStatus(s) == WireCollectionComplete
}

// sourceScopeKey builds the stable scope key for a client/scope pair.
func sourceScopeKey(clientFamily, scope string) string {
	return clientFamily + ":" + scope
}

// BuildReport converts scan results into a submittable report.
//
// Every scanned scope appears in Scopes, including failures and absences. A
// scope that produced nothing is REPORTED, never omitted: omission is
// indistinguishable from success downstream and would silently inflate
// coverage. Assertions are only emitted for scopes that actually parsed, so a
// failed scope contributes coverage loss without contributing phantom absence.
func BuildReport(
	collector CollectorIdentity,
	scanID string,
	startedAt, completedAt time.Time,
	results []ConfigScanResult,
) DiscoveryReport {
	report := DiscoveryReport{
		SchemaVersion:         ReportSchemaVersion,
		Collector:             collector,
		ScanID:                scanID,
		CollectionStartedAt:   startedAt.UTC().Format(time.RFC3339Nano),
		CollectionCompletedAt: completedAt.UTC().Format(time.RFC3339Nano),
		Scopes:                make([]CollectionScope, 0, len(results)),
		ResourceAssertions:    make([]ResourceAssertion, 0),
	}

	observedAt := completedAt.UTC().Format(time.RFC3339Nano)

	for _, result := range results {
		scopeKey := sourceScopeKey(result.ClientFamily, result.Scope)

		report.Scopes = append(report.Scopes, CollectionScope{
			SourceScope:       scopeKey,
			ProviderNamespace: result.ClientFamily,
			PathClass:         result.PathClass,
			CollectionStatus:  wireCollectionStatus(result.Status),
			ErrorClass:        result.ErrorClass,
		})

		// A scope we could not read tells us nothing about its contents. Any
		// servers on a non-OK result would be partial garbage, so they are not
		// promoted into assertions.
		if result.Status != ScanOK {
			continue
		}

		for _, server := range result.Servers {
			report.ResourceAssertions = append(report.ResourceAssertions, ResourceAssertion{
				SourceScope: scopeKey,
				// Instance-level identity: the same logical server configured by
				// two employees must remain two assertions, so the scope is part
				// of the key rather than the server name alone.
				SourceAssertionID: fmt.Sprintf("%s|%s", scopeKey, server.ServerName),
				ProviderNamespace: result.ClientFamily,
				ResourceKind:      "AI_ESTATE_RESOURCE_KIND_MCP_CLIENT_CONFIGURATION",
				ServerName:        server.ServerName,
				Transport:         server.Transport,
				EndpointHost:      server.EndpointHost,
				CommandIdentity:   server.CommandIdentity,
				ArgCount:          server.RawArgCount,
				CredentialClass:   wireCredentialClass(server.CredentialClass),
				CredentialKeyHint: server.CredentialHint,
				// Default. A configuration we merely found is unmediated until
				// something proves otherwise; ApplyManagedEndpoints upgrades the
				// ones that point at Telara. Defaulting to UNKNOWN instead would
				// make an unmanaged direct path indistinguishable from one we
				// simply failed to classify, which is the exact finding this
				// feature exists to surface.
				ControlState: WireControlDiscoveredUnmediated,
				ObservedAt:   observedAt,
			})
		}
	}

	// Deterministic ordering so a re-scan of unchanged config produces a
	// byte-identical report and downstream idempotency is exercisable.
	sort.Slice(report.Scopes, func(i, j int) bool {
		return report.Scopes[i].SourceScope < report.Scopes[j].SourceScope
	})
	sort.Slice(report.ResourceAssertions, func(i, j int) bool {
		return report.ResourceAssertions[i].SourceAssertionID < report.ResourceAssertions[j].SourceAssertionID
	})

	return report
}

// CoverageSummary is what the /ai-estate coverage rail needs in order to state
// plainly what was and was not covered.
type CoverageSummary struct {
	ScopesTotal    int `json:"scopesTotal"`
	ScopesComplete int `json:"scopesComplete"`
	ScopesPartial  int `json:"scopesPartial"`
	ScopesFailed   int `json:"scopesFailed"`
}

// Summarize counts scope outcomes. Kept separate from BuildReport so coverage
// can never be recomputed from assertions — deriving coverage from the things
// we found would report a totally failed scan as perfect coverage of nothing.
func Summarize(report DiscoveryReport) CoverageSummary {
	summary := CoverageSummary{ScopesTotal: len(report.Scopes)}
	for _, scope := range report.Scopes {
		switch scope.CollectionStatus {
		case WireCollectionComplete:
			summary.ScopesComplete++
		case WireCollectionPartial:
			summary.ScopesPartial++
		case WireCollectionFailed:
			summary.ScopesFailed++
		}
	}
	return summary
}

// ApplyManagedEndpoints upgrades assertions whose endpoint host matches a host
// Telara itself manages, from DISCOVERED_UNMEDIATED to DISCOVERED_MANAGED.
//
// This is kept separate from BuildReport because the collector package has no
// business knowing the tenant's API URL — the caller does. Matching is on the
// normalized endpoint HOST, never on the server's display name: a config can
// call itself anything, and name-derived conclusions are not evidence.
//
// stdio servers are never upgraded. A local command is by definition not
// reached through the Telara gateway.
func ApplyManagedEndpoints(report *DiscoveryReport, managedHosts []string) {
	if report == nil || len(managedHosts) == 0 {
		return
	}
	managed := make(map[string]bool, len(managedHosts))
	for _, h := range managedHosts {
		if normalized := NormalizeEndpointHost(h); normalized != "" && normalized != "unknown" {
			managed[normalized] = true
		}
	}
	if len(managed) == 0 {
		return
	}
	for i := range report.ResourceAssertions {
		a := &report.ResourceAssertions[i]
		if a.Transport == TransportStdio || a.EndpointHost == "" {
			continue
		}
		if managed[a.EndpointHost] {
			a.ControlState = WireControlDiscoveredManaged
		}
	}
}
