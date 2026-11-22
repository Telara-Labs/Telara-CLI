package mcpquarantine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// A Goose config with comments, a flow sequence of args, a block map of envs
// (plain and double-quoted), a flow map of headers (single-quoted), a uri with
// a query, and another client's extension.
var gooseYAML = `# goose config
GOOSE_PROVIDER: openai
extensions:
  gdrive:
    name: Google Drive   # display name
    type: stdio
    cmd: npx
    args: ["-y", gdrive-mcp, --refresh-token, '` + refreshToken + `']
    envs:
      GOOGLE_CLIENT_ID: ` + googleClient + `
      GOOGLE_CLIENT_SECRET: "` + clientSecret + `"   # keep this comment
      LOG: debug
    enabled: true
  calendar:
    type: streamable_http
    uri: https://cal.example.test/mcp?client_id=` + googleClient + `&access_token=` + encodedRaw + `&x=1
    headers: {Authorization: 'Bearer ` + bearerToken + `', X-Trace: on}
  gmail:
    type: stdio
    cmd: gmail-mcp
    envs: {GOOGLE_CLIENT_ID: ` + otherClient + `, GOOGLE_REFRESH_TOKEN: "1//0gOtherClientRefresh"}
`

// A Continue config: mcpServers is a list named by "name", with env and
// requestOptions.headers.
var continueYAML = `name: my config
version: 0.0.1
mcpServers:
  - name: gdrive
    command: npx
    env:
      GOOGLE_OAUTH_CLIENT_ID: ` + googleClient + `
      GOOGLE_REFRESH_TOKEN: ` + refreshToken + `  # trailing comment
  - name: remote
    type: streamable-http
    url: "https://r.example.test/mcp?key=KeyValue42&client_id=` + googleClient + `"
    requestOptions:
      headers:
        X-Api-Key: SecretApiKey42
`

func yamlHome(t *testing.T) (home, goose, cont string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "no-gcloud"))
	t.Setenv("XDG_CONFIG_HOME", "")
	goose = filepath.Join(home, ".config", "goose", "config.yaml")
	cont = filepath.Join(home, ".continue", "config.yaml")
	write(t, goose, gooseYAML)
	write(t, cont, continueYAML)
	return home, goose, cont
}

// rebuild puts each record's original bytes back in place of its placeholder.
func rebuild(t *testing.T, home, path, text string) string {
	t.Helper()
	for _, r := range records(t, home) {
		if r.Path != path {
			continue
		}
		if r.Format != "yaml" || r.OriginalRaw == "" {
			t.Fatalf("record %+v", r)
		}
		placed := string(encodeSpanValue("yaml", r.Placeholder))
		if r.Param != "" {
			placed = r.Placeholder
		}
		text = strings.Replace(text, placed, r.OriginalRaw, 1)
	}
	return text
}

func TestYAMLStripIsByteStableAndRestoresExactly(t *testing.T) {
	home, goose, cont := yamlHome(t)
	strips := []CredentialStrip{{ClientID: googleClient}}
	acts, err := SyncCredentials(home, strips, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range kinds(acts, "credential_quarantined") {
		got = append(got, a.ClientID+" "+a.Server+" "+strings.Fields(a.Note)[0])
	}
	want := []string{
		"continue_dev gdrive env.GOOGLE_REFRESH_TOKEN",
		"continue_dev remote requestOptions.headers.X-Api-Key",
		"continue_dev remote url?key",
		"goose calendar headers.Authorization",
		"goose calendar uri?access_token",
		"goose gdrive args[3]",
		"goose gdrive envs.GOOGLE_CLIENT_SECRET",
	}
	if strings.Join(sortedCopy(got), "\n") != strings.Join(want, "\n") {
		t.Fatalf("took out\n%s", strings.Join(sortedCopy(got), "\n"))
	}
	for path, orig := range map[string]string{goose: gooseYAML, cont: continueYAML} {
		text := mustRead(t, path)
		for _, secret := range []string{refreshToken, clientSecret, bearerToken, encodedRaw, "KeyValue42", "SecretApiKey42"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s still prints %s:\n%s", path, secret, text)
			}
		}
		if rebuild(t, home, path, text) != orig {
			t.Fatalf("the edit of %s was not confined to the values:\n%s", path, text)
		}
	}
	text := mustRead(t, goose)
	if !strings.Contains(text, "# keep this comment") || !strings.Contains(text, "# display name") || !strings.Contains(text, `"1//0gOtherClientRefresh"`) ||
		!strings.Contains(text, "&x=1\n") || !strings.Contains(text, "?client_id="+googleClient+"&access_token=telara-quarantined:") {
		t.Fatalf("a comment, another client's token or the rest of the URL changed:\n%s", text)
	}
	if again, err := SyncCredentials(home, strips, time.Now()); err != nil || len(again) != 0 {
		t.Fatalf("re-scan acted: %+v %v", again, err)
	}
	// The placeholders still read as YAML strings the client can load.
	if doc, err := yamlSpans([]byte(text), "extensions", "_key"); err != nil || len(doc.order) != 3 {
		t.Fatalf("stripped file does not parse: %v", err)
	}
	if _, err := SyncCredentials(home, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, goose) != gooseYAML || mustRead(t, cont) != continueYAML {
		t.Fatalf("not restored byte for byte:\n%s\n%s", mustRead(t, goose), mustRead(t, cont))
	}
	if len(records(t, home)) != 0 {
		t.Fatal("records left after restore")
	}
}

func TestYAMLHandChangedValueIsAConflict(t *testing.T) {
	home, goose, _ := yamlHome(t)
	if _, err := SyncCredentials(home, []CredentialStrip{{TokenSHA256: sha256Hex(clientSecret)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	text := mustRead(t, goose)
	start := strings.Index(text, `"telara-quarantined:`)
	end := start + 1 + strings.Index(text[start+1:], `"`) + 1
	write(t, goose, text[:start]+`new-secret-by-hand`+text[end:])
	acts, err := SyncCredentials(home, nil, time.Now())
	if err != nil || len(kinds(acts, "credential_conflict")) != 1 {
		t.Fatalf("want a conflict, got %+v %v", acts, err)
	}
	if !strings.Contains(mustRead(t, goose), "new-secret-by-hand") || len(records(t, home)) != 1 {
		t.Fatal("the hand edit was overwritten or the record dropped")
	}
}

func refusedNotes(acts []Action) []string {
	var out []string
	for _, a := range kinds(acts, "credential_unhandled") {
		out = append(out, a.Server+" "+a.Note)
	}
	return out
}

// Values that cannot be edited byte for byte are still matched, and a match
// is refused loudly with the whole file left unchanged.
func TestYAMLUneditableFormsAreRefused(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"block scalar": {`extensions:
  gdrive:
    cmd: npx
    envs:
      GOOGLE_CLIENT_ID: ` + googleClient + `
      GOOGLE_REFRESH_TOKEN: |
        ` + refreshToken + `
`, "gdrive envs.GOOGLE_REFRESH_TOKEN: the value is a YAML block scalar"},
		"anchored map": {`common: &creds
  GOOGLE_CLIENT_ID: ` + googleClient + `
  GOOGLE_REFRESH_TOKEN: ` + refreshToken + `
extensions:
  gdrive:
    cmd: npx
    envs: *creds
`, "gdrive envs.GOOGLE_REFRESH_TOKEN: envs is an alias of an anchor used elsewhere in the file"},
		"merge key": {`base: &base
  GOOGLE_REFRESH_TOKEN: ` + refreshToken + `
extensions:
  gdrive:
    cmd: npx
    envs:
      <<: *base
      GOOGLE_CLIENT_ID: ` + googleClient + `
`, "gdrive envs.GOOGLE_REFRESH_TOKEN: the value comes in through a merge key"},
		"folded plain": {`extensions:
  gdrive:
    cmd: npx
    envs:
      GOOGLE_CLIENT_ID: ` + googleClient + `
      GOOGLE_REFRESH_TOKEN: ` + refreshToken + `
        continued
`, "gdrive envs.GOOGLE_REFRESH_TOKEN: the value's source bytes could not be located exactly"},
		"escaped url": {"extensions:\n  remote:\n    uri: \"https://r.test/mcp?client_id=" + googleClient + "&token=a\\x41b\"\n",
			"remote uri?token: " + escapedURLReason},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home, goose, cont := yamlHome(t)
			write(t, goose, tc.body)
			acts, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient}}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			notes := refusedNotes(acts)
			if len(notes) != 1 || !strings.HasPrefix(notes[0], tc.want) {
				t.Fatalf("want one refusal %q, got %v", tc.want, notes)
			}
			if mustRead(t, goose) != tc.body {
				t.Fatal("a refused file was edited")
			}
			for _, r := range records(t, home) {
				if r.Path == goose {
					t.Fatal("a record was written for a refused file")
				}
			}
			// Continue's file is unaffected by Goose's refusal.
			if mustRead(t, cont) == continueYAML {
				t.Fatal("the other file was not stripped")
			}
			bs, complete := CredentialBindings(home, nil)
			if !complete {
				t.Fatal("a refused value is read, so the scope is complete")
			}
			found := false
			for _, b := range bs {
				if b.Client == discovery.ClientGoose && b.OAuthClientID == googleClient {
					found = true
					if b.State != discovery.CredentialStateUnhandled {
						t.Fatalf("binding %+v, want unhandled", b)
					}
				}
			}
			if !found {
				t.Fatalf("no Goose binding in %+v", bs)
			}
		})
	}
}

func TestYAMLUnreadableShapesAreRefused(t *testing.T) {
	cases := map[string]string{
		"two documents":   "extensions: {}\n---\nextensions: {}\n",
		"duplicate names": "",
		"not yaml":        "extensions: [unclosed\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home, goose, cont := yamlHome(t)
			path := goose
			if name == "duplicate names" {
				path = cont
				body = "mcpServers:\n  - name: a\n    env: {GOOGLE_CLIENT_ID: " + googleClient + ", TOKEN: x1}\n  - name: a\n    command: y\n"
			}
			write(t, path, body)
			acts, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient}}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var mine []Action
			for _, a := range kinds(acts, "credential_unhandled") {
				if a.Path == path {
					mine = append(mine, a)
				}
			}
			if len(mine) != 1 || mustRead(t, path) != body {
				t.Fatalf("want one refusal and an unchanged file, got %+v", mine)
			}
		})
	}
}

func TestGooseSecretsFileIsRefusedAndKeepsBindingsIncomplete(t *testing.T) {
	home, _, _ := yamlHome(t)
	if _, complete := CredentialBindings(home, nil); !complete {
		t.Fatal("incomplete before the secrets file exists")
	}
	write(t, filepath.Join(home, ".config", "goose", "secrets.yaml"), "GOOGLE_REFRESH_TOKEN: "+refreshToken+"\n")
	acts, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range kinds(acts, "credential_unhandled") {
		found = found || strings.HasSuffix(a.Path, "secrets.yaml")
	}
	if !found {
		t.Fatalf("secrets.yaml not reported: %+v", acts)
	}
	if _, complete := CredentialBindings(home, nil); complete {
		t.Fatal("a file no strip reads must keep the scope incomplete")
	}
}

func TestYAMLBindingsPresentThenQuarantined(t *testing.T) {
	home, _, _ := yamlHome(t)
	state := func() map[string]string {
		bs, complete := CredentialBindings(home, nil)
		if !complete {
			t.Fatal("incomplete")
		}
		out := map[string]string{}
		for _, b := range bs {
			out[b.Client+"|"+b.Server+"|"+b.OAuthClientID] = b.State
		}
		return out
	}
	keys := []string{"goose|gdrive|" + googleClient, "goose|calendar|" + googleClient, "continue_dev|gdrive|" + googleClient, "continue_dev|remote|" + googleClient}
	before := state()
	for _, k := range keys {
		if before[k] != discovery.CredentialStatePresent {
			t.Fatalf("%s before: %q (%v)", k, before[k], before)
		}
	}
	if before["goose|gmail|"+otherClient] != discovery.CredentialStatePresent {
		t.Fatalf("other client: %v", before)
	}
	if _, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	after := state()
	for _, k := range keys {
		if after[k] != discovery.CredentialStateQuarantined {
			t.Fatalf("%s after: %q", k, after[k])
		}
	}
	if after["goose|gmail|"+otherClient] != discovery.CredentialStatePresent {
		t.Fatal("another client's binding changed")
	}
}
