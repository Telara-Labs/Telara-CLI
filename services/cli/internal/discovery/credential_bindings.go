package discovery

import (
	"fmt"
	"sort"
	"strings"
)

// Credential bindings (TENG-3921). Block
// telara-documentation/architecture/estate-remediation.md ("Credential strips").
//
// A scan reports, for each OAuth client whose credential it finds in a file it
// reads, one CREDENTIAL_BINDING assertion per place: which client (the MCP
// client, or gcloud), which server entry, the OAuth client id, and whether the
// credential is present or was taken out by a credential strip. This is how a
// takedown of an OAuth client learns which devices hold it and whether each has
// taken it out. No credential value is ever reported, only key names.

// CredentialBindingScope is the one scope every binding sits in. It is
// complete only when every credential file was read, so a failed read never
// withdraws a binding.
const CredentialBindingScope = "local_credentials:global"

// Credential states.
const (
	CredentialStatePresent     = "present"
	CredentialStateQuarantined = "quarantined"
	// CredentialStateUnhandled is a credential a strip would take out but
	// cannot edit byte for byte (a YAML block scalar, an alias, an escaped
	// URL). It is still in the file, so it never counts as done.
	CredentialStateUnhandled = "unhandled"
)

// CredentialBinding is one OAuth client's credential at one place.
type CredentialBinding struct {
	Client        string   // MCP client id (claude-code, openai-codex) or gcloud
	Server        string   // server entry key; empty for a gcloud file
	OAuthClientID string   // the OAuth client the credential belongs to
	State         string   // present | quarantined | unhandled
	Locations     []string // key names only, e.g. env.GOOGLE_REFRESH_TOKEN
}

// AppendCredentialBindings adds the bindings scope and, when it is complete,
// one assertion per binding.
func AppendCredentialBindings(report *DiscoveryReport, bindings []CredentialBinding, complete bool, observedAt string) {
	status := WireCollectionComplete
	if !complete {
		status = WireCollectionFailed
	}
	report.Scopes = append(report.Scopes, CollectionScope{
		SourceScope: CredentialBindingScope, ProviderNamespace: "local_credentials", PathClass: "user_global", CollectionStatus: status,
	})
	if complete {
		for _, b := range bindings {
			class := WireCredentialInline
			if b.State == CredentialStateQuarantined {
				class = WireCredentialNone
			}
			// The gateway accepts present and quarantined only
			// (telara-middleware ai_estate_endpoints.go). An unhandled
			// credential is still in the file, so it travels as present,
			// which knowledge's fan-out never counts as done.
			state := b.State
			if state == CredentialStateUnhandled {
				state = CredentialStatePresent
			}
			locations := append([]string(nil), b.Locations...)
			sort.Strings(locations)
			report.ResourceAssertions = append(report.ResourceAssertions, ResourceAssertion{
				SourceScope:       CredentialBindingScope,
				SourceAssertionID: fmt.Sprintf("%s|%s|%s|%s", CredentialBindingScope, b.Client, b.Server, b.OAuthClientID),
				ProviderNamespace: b.Client,
				ResourceKind:      ResourceKindCredentialBinding,
				ServerName:        b.OAuthClientID,
				Transport:         TransportUnknown,
				CredentialClass:   class,
				CredentialKeyHint: strings.Join(locations, ","),
				ControlState:      WireControlDiscoveredUnmediated,
				ObservedAt:        observedAt,
				OAuthClientID:     b.OAuthClientID,
				CredentialState:   state,
			})
		}
	}
	sort.Slice(report.Scopes, func(i, j int) bool { return report.Scopes[i].SourceScope < report.Scopes[j].SourceScope })
	sort.Slice(report.ResourceAssertions, func(i, j int) bool {
		return report.ResourceAssertions[i].SourceAssertionID < report.ResourceAssertions[j].SourceAssertionID
	})
}
