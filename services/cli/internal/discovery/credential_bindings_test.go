package discovery

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendCredentialBindings(t *testing.T) {
	const cid = "123456789-abcdef.apps.googleusercontent.com"
	bindings := []CredentialBinding{
		{Client: "claude-code", Server: "gdrive", OAuthClientID: cid, State: CredentialStatePresent, Locations: []string{"env.GOOGLE_REFRESH_TOKEN", "args[5]"}},
		{Client: GoogleADCClient, OAuthClientID: cid, State: CredentialStateQuarantined, Locations: []string{"json.refresh_token"}},
	}
	var r DiscoveryReport
	AppendCredentialBindings(&r, bindings, true, "2026-11-21T09:00:00Z")
	if len(r.Scopes) != 1 || r.Scopes[0].SourceScope != CredentialBindingScope || r.Scopes[0].CollectionStatus != WireCollectionComplete {
		t.Fatalf("scopes %+v", r.Scopes)
	}
	if len(r.ResourceAssertions) != 2 {
		t.Fatalf("assertions %+v", r.ResourceAssertions)
	}
	for _, a := range r.ResourceAssertions {
		if a.ResourceKind != ResourceKindCredentialBinding || a.ServerName != cid || a.OAuthClientID != cid {
			t.Fatalf("assertion %+v", a)
		}
		switch a.CredentialState {
		case CredentialStatePresent:
			if a.CredentialClass != WireCredentialInline || a.CredentialKeyHint != "args[5],env.GOOGLE_REFRESH_TOKEN" || a.SourceAssertionID != CredentialBindingScope+"|claude-code|gdrive|"+cid {
				t.Fatalf("present %+v", a)
			}
		case CredentialStateQuarantined:
			if a.CredentialClass != WireCredentialNone {
				t.Fatalf("quarantined %+v", a)
			}
		default:
			t.Fatalf("state %q", a.CredentialState)
		}
	}
	body, _ := json.Marshal(r)
	if !strings.Contains(string(body), `"oauthClientId"`) || !strings.Contains(string(body), `"credentialState"`) {
		t.Fatalf("wire %s", body)
	}

	var failed DiscoveryReport
	AppendCredentialBindings(&failed, bindings, false, "2026-11-21T09:00:00Z")
	if failed.Scopes[0].CollectionStatus != WireCollectionFailed || len(failed.ResourceAssertions) != 0 {
		t.Fatal("an incomplete read must report a failed scope and no assertions, so nothing is withdrawn")
	}
}

// An unhandled credential is still in the file: it travels as present (the
// only not-done state the gateway accepts), never as quarantined.
func TestUnhandledBindingTravelsAsPresent(t *testing.T) {
	const cid = "123456789-abcdef.apps.googleusercontent.com"
	var r DiscoveryReport
	AppendCredentialBindings(&r, []CredentialBinding{{Client: ClientGoose, Server: "gdrive", OAuthClientID: cid, State: CredentialStateUnhandled,
		Locations: []string{"envs.GOOGLE_REFRESH_TOKEN"}}}, true, "2026-11-21T09:00:00Z")
	if len(r.ResourceAssertions) != 1 || r.ResourceAssertions[0].CredentialState != CredentialStatePresent || r.ResourceAssertions[0].CredentialClass != WireCredentialInline {
		t.Fatalf("got %+v", r.ResourceAssertions)
	}
}
