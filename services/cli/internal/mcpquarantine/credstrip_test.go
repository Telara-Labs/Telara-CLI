package mcpquarantine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	googleClient = "123456789-abcdef.apps.googleusercontent.com"
	otherClient  = "987654321-zyxwvu.apps.googleusercontent.com"
	refreshToken = "1//0gRefreshTokenValue-abc123"
	clientSecret = "GOCSPX-clientsecretvalue42"
	bearerToken  = "ya29.a0AccessTokenValue987"
)

// credClaudeJSON holds one server carrying the listed Google client, one
// carrying another client, and one with a bearer header and no client id.
var credClaudeJSON = `{
  "numStartups": 3,
  "mcpServers": {
    "gdrive": {
      "command": "npx",
      "args": ["-y", "gdrive-mcp", "--client-id", "` + googleClient + `", "--refresh-token", "` + refreshToken + `", "GOOGLE_CLIENT_SECRET=` + clientSecret + `"],
      "env": {"GOOGLE_OAUTH_CLIENT_ID": "` + googleClient + `", "GOOGLE_REFRESH_TOKEN": "` + refreshToken + `", "GOOGLE_TOKEN_FILE": "/Users/x/.config/token.json", "API_TOKEN": "${API_TOKEN}", "LOG_LEVEL": "debug"}
    },
    "gmail": {
      "command": "gmail-mcp",
      "env": {"GOOGLE_CLIENT_ID": "` + otherClient + `", "GOOGLE_REFRESH_TOKEN": "1//0gOtherClientRefresh"}
    },
    "calendar": {
      "type": "http",
      "url": "https://example.test/mcp",
      "headers": {"Authorization": "Bearer ` + bearerToken + `", "X-Trace": "on"}
    }
  }
}`

func credEntry(t *testing.T, path, server string) map[string]any {
	t.Helper()
	var e map[string]any
	if err := json.Unmarshal(servers(t, path)[server], &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func records(t *testing.T, home string) []CredentialRecord {
	t.Helper()
	entries, err := os.ReadDir(CredentialRoot(home))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []CredentialRecord
	for _, e := range entries {
		r, err := readCredentialRecord(filepath.Join(CredentialRoot(home), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestParseCredentialStrips(t *testing.T) {
	fp := sha256Hex("x")
	got, err := ParseCredentialStrips(`[{"client_id":"` + googleClient + `"},{"token_sha256":"` + fp + `"}]`)
	if err != nil || len(got) != 2 || got[0].ClientID != googleClient || got[1].TokenSHA256 != fp {
		t.Fatalf("got %+v %v", got, err)
	}
	if got, err := ParseCredentialStrips(" "); err != nil || len(got) != 0 {
		t.Fatalf("empty: %+v %v", got, err)
	}
	for _, bad := range []string{`{"client_id":"a"}`, `[{}]`, `[{"client_id":"a b"}]`, `[{"token_sha256":"ABC"}]`, `[{"client_id":"a","secret":"s"}]`, `["a"]`} {
		if _, err := ParseCredentialStrips(bad); err == nil {
			t.Fatalf("%s parsed", bad)
		}
	}
}

func TestClientIDMatchTakesOutOnlyThatClientsSecrets(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, credClaudeJSON)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	strips := []CredentialStrip{{ClientID: googleClient}}

	actions, err := SyncCredentials(home, strips, now)
	if err != nil {
		t.Fatal(err)
	}
	var locs []string
	for _, a := range actions {
		if a.Kind != "credential_quarantined" || a.Server != "gdrive" || a.ClientID != "claude-code" {
			t.Fatalf("unexpected action %+v", a)
		}
		locs = append(locs, strings.Fields(a.Note)[0])
		if strings.Contains(a.Note, refreshToken) || strings.Contains(a.Note, clientSecret) {
			t.Fatal("a report carries the credential value")
		}
	}
	want := "env.GOOGLE_REFRESH_TOKEN args[5] args[6]"
	if strings.Join(sortedCopy(locs), " ") != strings.Join(sortedCopy(strings.Fields(want)), " ") {
		t.Fatalf("took out %v, want %s", locs, want)
	}
	text := mustRead(t, claude)
	for _, secret := range []string{refreshToken, clientSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s is still printed in the file:\n%s", secret, text)
		}
	}
	gd := credEntry(t, claude, "gdrive")
	env := gd["env"].(map[string]any)
	if env["GOOGLE_OAUTH_CLIENT_ID"] != googleClient {
		t.Fatal("the client id itself was taken out")
	}
	if env["GOOGLE_TOKEN_FILE"] != "/Users/x/.config/token.json" || env["API_TOKEN"] != "${API_TOKEN}" || env["LOG_LEVEL"] != "debug" {
		t.Fatalf("a path, a reference or a plain value was touched: %+v", env)
	}
	if !strings.HasPrefix(env["GOOGLE_REFRESH_TOKEN"].(string), "telara-quarantined:") {
		t.Fatalf("no placeholder: %v", env["GOOGLE_REFRESH_TOKEN"])
	}
	args := gd["args"].([]any)
	if args[3] != googleClient || !strings.HasPrefix(args[6].(string), "GOOGLE_CLIENT_SECRET=telara-quarantined:") {
		t.Fatalf("args: %v", args)
	}
	if credEntry(t, claude, "gmail")["env"].(map[string]any)["GOOGLE_REFRESH_TOKEN"] != "1//0gOtherClientRefresh" {
		t.Fatal("another client's token was taken out")
	}
	if !strings.Contains(credEntry(t, claude, "calendar")["headers"].(map[string]any)["Authorization"].(string), bearerToken) {
		t.Fatal("a server without the client id was touched")
	}
	if !strings.Contains(text, `"numStartups": 3`) {
		t.Fatal("unrelated content changed")
	}

	// Owner-only records holding the exact values.
	st, err := os.Stat(CredentialRoot(home))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("credential root mode %v %v", st.Mode().Perm(), err)
	}
	recs := records(t, home)
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d", len(recs))
	}
	originals := map[string]string{}
	for _, r := range recs {
		fi, err := os.Stat(filepath.Join(CredentialRoot(home), r.ID+".json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("record %s mode %v %v", r.ID, fi.Mode().Perm(), err)
		}
		originals[r.Location()] = r.Original
	}
	if originals["env.GOOGLE_REFRESH_TOKEN"] != refreshToken || originals["args[5]"] != refreshToken || originals["args[6]"] != "GOOGLE_CLIENT_SECRET="+clientSecret {
		t.Fatalf("records: %+v", originals)
	}
}

func TestFingerprintMatchesWithoutAClientID(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, credClaudeJSON)
	actions, err := SyncCredentials(home, []CredentialStrip{{TokenSHA256: sha256Hex(bearerToken)}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Server != "calendar" || !strings.HasPrefix(actions[0].Note, "headers.Authorization") {
		t.Fatalf("got %+v", actions)
	}
	if strings.Contains(mustRead(t, claude), bearerToken) {
		t.Fatal("the bearer token is still printed")
	}
	if credEntry(t, claude, "calendar")["headers"].(map[string]any)["X-Trace"] != "on" {
		t.Fatal("another header changed")
	}
}

func TestFingerprintWithClientIDNeedsTheClientAlongside(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, credClaudeJSON)
	before := mustRead(t, claude)
	// The bearer token's fingerprint, scoped to a client its server does not carry.
	actions, err := SyncCredentials(home, []CredentialStrip{{ClientID: googleClient, TokenSHA256: sha256Hex(bearerToken)}, {ClientID: "unrelated-client.apps.googleusercontent.com"}}, time.Now())
	if err != nil || len(actions) != 0 {
		t.Fatalf("got %+v %v", actions, err)
	}
	if mustRead(t, claude) != before {
		t.Fatal("the file changed although nothing matched")
	}
}

func TestRestorePutsTheExactValueBackAndRescanIsIdempotent(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, credClaudeJSON)
	strips := []CredentialStrip{{ClientID: googleClient}, {TokenSHA256: sha256Hex(bearerToken)}}
	if _, err := SyncCredentials(home, strips, time.Now()); err != nil {
		t.Fatal(err)
	}
	stripped := mustRead(t, claude)

	// Idempotent: the same list again changes nothing and writes no new record.
	again, err := SyncCredentials(home, strips, time.Now())
	if err != nil || len(again) != 0 {
		t.Fatalf("re-scan acted: %+v %v", again, err)
	}
	if mustRead(t, claude) != stripped || len(records(t, home)) != 4 {
		t.Fatal("a re-scan changed the file or the records")
	}

	// Lift the client strip only: its three values come back, the bearer stays out.
	acts, err := SyncCredentials(home, strips[1:], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	restored := 0
	for _, a := range acts {
		if a.Kind == "credential_restored" {
			restored++
		}
	}
	if restored != 3 {
		t.Fatalf("want 3 restored, got %+v", acts)
	}
	gd := credEntry(t, claude, "gdrive")
	if gd["env"].(map[string]any)["GOOGLE_REFRESH_TOKEN"] != refreshToken {
		t.Fatal("env value not restored exactly")
	}
	args := gd["args"].([]any)
	if args[5] != refreshToken || args[6] != "GOOGLE_CLIENT_SECRET="+clientSecret {
		t.Fatalf("args not restored exactly: %v", args)
	}
	if strings.Contains(mustRead(t, claude), bearerToken) {
		t.Fatal("a still-listed credential came back")
	}

	// Lift everything: the file is back to its original content.
	if _, err := SyncCredentials(home, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(records(t, home)) != 0 {
		t.Fatal("records left after every strip was lifted")
	}
	var want, got any
	_ = json.Unmarshal([]byte(credClaudeJSON), &want)
	_ = json.Unmarshal([]byte(mustRead(t, claude)), &got)
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if string(wb) != string(gb) {
		t.Fatalf("not restored to the original:\n%s\n%s", wb, gb)
	}
	if !strings.Contains(mustRead(t, claude), `"numStartups"`) || strings.Index(mustRead(t, claude), `"gdrive"`) > strings.Index(mustRead(t, claude), `"calendar"`) {
		t.Fatal("key order changed")
	}
}

func TestRestoreRefusesWhenTheValueWasChanged(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, credClaudeJSON)
	strips := []CredentialStrip{{TokenSHA256: sha256Hex(bearerToken)}}
	if _, err := SyncCredentials(home, strips, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The person enters a new token by hand while the old one is out.
	text := mustRead(t, claude)
	start := strings.Index(text, "telara-quarantined:")
	end := start + strings.Index(text[start:], `"`)
	write(t, claude, text[:start]+"Bearer brand-new-token-1"+text[end:])

	acts, err := SyncCredentials(home, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Kind != "credential_conflict" {
		t.Fatalf("want one conflict, got %+v", acts)
	}
	if !strings.Contains(mustRead(t, claude), "brand-new-token-1") || strings.Contains(mustRead(t, claude), bearerToken) {
		t.Fatal("the hand-entered value was overwritten")
	}
	if recs := records(t, home); len(recs) != 1 || recs[0].Original != "Bearer "+bearerToken {
		t.Fatal("the record was not kept")
	}

	// A server entry removed by hand is also a conflict, and the record stays.
	write(t, claude, `{"mcpServers": {}}`)
	acts, _ = SyncCredentials(home, nil, time.Now())
	if len(acts) != 1 || acts[0].Kind != "credential_conflict" || len(records(t, home)) != 1 {
		t.Fatalf("got %+v", acts)
	}
}

func TestRestoreAfterAFailedRewriteKeepsTheValue(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, credClaudeJSON)
	strips := []CredentialStrip{{TokenSHA256: sha256Hex(bearerToken)}}
	if _, err := SyncCredentials(home, strips, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Simulate a rewrite that never landed: the original file is back.
	write(t, claude, credClaudeJSON)
	again, err := SyncCredentials(home, strips, time.Now())
	if err != nil || len(again) != 1 || again[0].Kind != "credential_quarantined" || len(records(t, home)) != 1 {
		t.Fatalf("the existing record was not reused: %+v %v", again, err)
	}
}

func TestPlaceholderIsNotReportedAsAnInlineSecret(t *testing.T) {
	if strippable("telara-quarantined:0123456789abcdef") || strippable("KEY=telara-quarantined:0123456789abcdef") {
		t.Fatal("a placeholder must never be taken out again")
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
