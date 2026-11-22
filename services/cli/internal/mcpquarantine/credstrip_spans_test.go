package mcpquarantine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// A Codex config with comments, a [table] env, an inline-table env, a literal
// string, an escaped string and args, plus another client's server.
var codexTOML = `# my codex config
model = "o4"

[mcp_servers.gdrive]
command = "npx"   # keep this comment
args = ["-y", "gdrive-mcp", "--refresh-token", '` + refreshToken + `']

[mcp_servers.gdrive.env]
GOOGLE_CLIENT_ID = "` + googleClient + `"
GOOGLE_CLIENT_SECRET = "` + clientSecret + `"
LOG = "debug"

[mcp_servers.gmail]
command = "gmail-mcp"
env = { GOOGLE_CLIENT_ID = "` + otherClient + `", GOOGLE_REFRESH_TOKEN = "1//0gOtherClientRefresh" }

[mcp_servers.calendar]
url = "https://example.test/mcp"
http_headers = { "Authorization" = "Bearer ` + bearerToken + `", X-Trace = "on!" }
`

var adcJSON = `{
  "account": "",
  "client_id": "` + googleClient + `",
  "client_secret": "` + clientSecret + `",
  "quota_project_id": "proj-1",
  "refresh_token": "` + refreshToken + `",
  "type": "authorized_user",
  "universe_domain": "googleapis.com"
}
`

func spanHome(t *testing.T) (home, codex, adc string) {
	t.Helper()
	t.Setenv("CLOUDSDK_CONFIG", "")
	home = t.TempDir()
	codex = filepath.Join(home, ".codex", "config.toml")
	adc = filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	write(t, codex, codexTOML)
	write(t, adc, adcJSON)
	return home, codex, adc
}

func kinds(acts []Action, kind string) []Action {
	var out []Action
	for _, a := range acts {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

func TestTOMLStripIsByteStableAndRestoresExactly(t *testing.T) {
	home, codex, _ := spanHome(t)
	strips := []CredentialStrip{{ClientID: googleClient}}
	acts, err := SyncCredentials(home, strips, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var codexActs []Action
	for _, a := range kinds(acts, "credential_quarantined") {
		if a.Path == codex {
			codexActs = append(codexActs, a)
		}
	}
	if len(codexActs) != 2 {
		t.Fatalf("want the args token and the env secret, got %+v", codexActs)
	}
	got := mustRead(t, codex)
	if strings.Contains(got, refreshToken) || strings.Contains(got, clientSecret) {
		t.Fatalf("a secret is still printed:\n%s", got)
	}
	// Every byte outside the two values is unchanged.
	recs := records(t, home)
	rebuilt := got
	for _, r := range recs {
		if r.Path != codex {
			continue
		}
		if r.Format != "toml" || r.OriginalRaw == "" {
			t.Fatalf("record %+v", r)
		}
		rebuilt = strings.Replace(rebuilt, string(encodeSpanValue("toml", r.Placeholder)), r.OriginalRaw, 1)
	}
	if rebuilt != codexTOML {
		t.Fatalf("the edit was not confined to the values:\n%s", got)
	}
	if !strings.Contains(got, "# keep this comment") || !strings.Contains(got, `"1//0gOtherClientRefresh"`) || !strings.Contains(got, googleClient) {
		t.Fatal("a comment, another client's token or the client id changed")
	}
	if again, err := SyncCredentials(home, strips, time.Now()); err != nil || len(again) != 0 {
		t.Fatalf("re-scan acted: %+v %v", again, err)
	}
	if _, err := SyncCredentials(home, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, codex) != codexTOML {
		t.Fatalf("not restored byte for byte:\n%s", mustRead(t, codex))
	}
}

func TestTOMLFingerprintAndConflict(t *testing.T) {
	home, codex, _ := spanHome(t)
	strips := []CredentialStrip{{TokenSHA256: sha256Hex(bearerToken)}}
	acts, err := SyncCredentials(home, strips, time.Now())
	if err != nil || len(acts) != 1 || acts[0].Server != "calendar" || !strings.HasPrefix(acts[0].Note, "http_headers.Authorization") {
		t.Fatalf("got %+v %v", acts, err)
	}
	// A new value entered by hand while it is out.
	text := mustRead(t, codex)
	start := strings.Index(text, `"telara-quarantined:`)
	end := start + 1 + strings.Index(text[start+1:], `"`) + 1
	write(t, codex, text[:start]+`"Bearer new-token-by-hand"`+text[end:])
	acts, err = SyncCredentials(home, nil, time.Now())
	if err != nil || len(kinds(acts, "credential_conflict")) != 1 {
		t.Fatalf("want a conflict, got %+v %v", acts, err)
	}
	if !strings.Contains(mustRead(t, codex), "new-token-by-hand") || len(records(t, home)) != 1 {
		t.Fatal("the hand edit was overwritten or the record dropped")
	}
}

func TestADCStripTakesOutRefreshTokenAndSecretOnly(t *testing.T) {
	home, _, adc := spanHome(t)
	acts, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var adcActs []string
	for _, a := range kinds(acts, "credential_quarantined") {
		if a.Path == adc {
			if a.ClientID != discovery.GoogleADCClient {
				t.Fatalf("client %q", a.ClientID)
			}
			adcActs = append(adcActs, strings.Fields(a.Note)[0])
		}
	}
	if strings.Join(sortedCopy(adcActs), " ") != "json.client_secret json.refresh_token" {
		t.Fatalf("took out %v", adcActs)
	}
	got := mustRead(t, adc)
	if strings.Contains(got, refreshToken) || strings.Contains(got, clientSecret) || !strings.Contains(got, googleClient) || !strings.Contains(got, `"quota_project_id": "proj-1"`) {
		t.Fatalf("adc after strip:\n%s", got)
	}
	fi, _ := os.Stat(adc)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("the file's own mode changed: %v", fi.Mode().Perm())
	}
	if _, err := SyncCredentials(home, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, adc) != adcJSON {
		t.Fatalf("not restored byte for byte:\n%s", mustRead(t, adc))
	}
}

func TestADCOtherClientOrServiceAccountIsUntouched(t *testing.T) {
	home, _, adc := spanHome(t)
	if acts, _ := SyncCredentials(home, []CredentialStrip{{ClientID: otherClient}}, time.Now()); len(kinds(acts, "credential_quarantined")) != 1 {
		// only the Codex gmail entry carries otherClient
		t.Fatalf("got %+v", acts)
	}
	if mustRead(t, adc) != adcJSON {
		t.Fatal("another client's gcloud credentials were touched")
	}
	sa := `{"type": "service_account", "client_id": "` + googleClient + `", "private_key": "-----BEGIN KEY-----"}`
	write(t, adc, sa)
	home2 := home
	if acts, _ := SyncCredentials(home2, []CredentialStrip{{ClientID: googleClient}}, time.Now()); func() bool {
		for _, a := range acts {
			if a.Path == adc {
				return true
			}
		}
		return false
	}() {
		t.Fatal("a service account key file was edited")
	}
	if mustRead(t, adc) != sa {
		t.Fatal("service account file changed")
	}
}

func TestCredentialBindingsReportPresentThenQuarantined(t *testing.T) {
	home, _, _ := spanHome(t)
	write(t, filepath.Join(home, ".claude.json"), credClaudeJSON)
	byKey := func(bs []discovery.CredentialBinding) map[string]discovery.CredentialBinding {
		out := map[string]discovery.CredentialBinding{}
		for _, b := range bs {
			out[b.Client+"|"+b.Server+"|"+b.OAuthClientID] = b
		}
		return out
	}
	bs, complete := CredentialBindings(home, nil)
	if !complete {
		t.Fatal("incomplete")
	}
	got := byKey(bs)
	for _, k := range []string{"claude-code|gdrive|" + googleClient, "codex|gdrive|" + googleClient, "codex|gmail|" + otherClient, "gcloud||" + googleClient, "claude-code|gmail|" + otherClient} {
		if got[k].State != discovery.CredentialStatePresent {
			t.Fatalf("%s: %+v (all %+v)", k, got[k], got)
		}
	}
	for _, b := range bs {
		for _, l := range b.Locations {
			if strings.Contains(l, refreshToken) || strings.Contains(l, clientSecret) {
				t.Fatal("a binding carries a value")
			}
		}
	}
	if _, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got = byKey(func() []discovery.CredentialBinding { b, _ := CredentialBindings(home, nil); return b }())
	for _, k := range []string{"claude-code|gdrive|" + googleClient, "codex|gdrive|" + googleClient, "gcloud||" + googleClient} {
		if got[k].State != discovery.CredentialStateQuarantined {
			t.Fatalf("%s after strip: %+v", k, got[k])
		}
	}
	if got["codex|gmail|"+otherClient].State != discovery.CredentialStatePresent {
		t.Fatal("another client's binding changed")
	}

	// A file that does not parse makes the scope incomplete.
	write(t, filepath.Join(home, ".codex", "config.toml"), "[mcp_servers\n")
	if _, complete := CredentialBindings(home, nil); complete {
		t.Fatal("an unparseable file must make the bindings incomplete")
	}
}
