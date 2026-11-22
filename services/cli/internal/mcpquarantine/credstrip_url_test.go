package mcpquarantine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueryParamsReadsEveryPairAndKeepsRawRanges(t *testing.T) {
	v := "https://h.test/p?token=a%2Fb+c&x&key=1&token=two&=nameless#frag?token=no"
	ps := queryParams(v)
	var got []string
	for _, p := range ps {
		got = append(got, p.name+"|"+p.value+"|"+v[p.start:p.end]+"|"+string(rune('0'+p.n)))
	}
	want := []string{"token|a/b c|a%2Fb+c|0", "key|1|1|0", "token|two|two|1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v", got)
	}
	if queryParams("not a url?token=x") != nil || queryParams("https://h.test/p#frag?token=x") != nil {
		t.Fatal("a non-URL or a fragment-only ? was read as a query")
	}
	if loc := (location{Field: "url", Index: -1, Param: "token", ParamN: 1}).String(); loc != "url?token[1]" {
		t.Fatalf("location %q", loc)
	}
}

const (
	encodedToken = "ya29.a0Tok/with space"
	encodedRaw   = "ya29.a0Tok%2Fwith+space"
)

var urlClaudeJSON = `{
  "mcpServers": {
    "remote": {
      "type": "http",
      "url": "https://mcp.example.test/sse?client_id=` + googleClient + `&access_token=` + encodedRaw + `&mode=x%20y",
      "headers": {"X-Trace": "on"}
    },
    "other": {
      "type": "http",
      "url": "https://mcp.example.test/sse?client_id=` + otherClient + `&access_token=1//0gOtherClientToken"
    },
    "wrapped": {
      "command": "proxy",
      "args": ["--url", "https://up.example.test/mcp?api_key=AIzaArgKey&client_id=` + googleClient + `"],
      "env": {"UPSTREAM_TOKEN_URL": "https://t.example.test/?client_id=` + googleClient + `&token=EnvURLTok"}
    }
  }
}`

func TestURLQueryStripTakesOnlyTheParameterAndRestoresExactly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "no-gcloud"))
	t.Setenv("XDG_CONFIG_HOME", "")
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, urlClaudeJSON)
	strips := []CredentialStrip{{ClientID: googleClient}}
	acts, err := SyncCredentials(home, strips, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, a := range kinds(acts, "credential_quarantined") {
		notes = append(notes, a.Server+" "+strings.Fields(a.Note)[0])
	}
	// The secret-named env URL goes whole (its query with it); the url field and
	// the argument URL lose only their secret parameter.
	want := []string{"remote url?access_token", "wrapped args[1]?api_key", "wrapped env.UPSTREAM_TOKEN_URL"}
	if strings.Join(sortedCopy(notes), ",") != strings.Join(want, ",") {
		t.Fatalf("took out %v", notes)
	}
	remote := credEntry(t, claude, "remote")["url"].(string)
	if strings.Contains(remote, encodedRaw) || !strings.HasPrefix(remote, "https://mcp.example.test/sse?client_id="+googleClient+"&access_token=telara-quarantined:") ||
		!strings.HasSuffix(remote, "&mode=x%20y") {
		t.Fatalf("remote url after strip %q", remote)
	}
	if got := credEntry(t, claude, "other")["url"].(string); !strings.Contains(got, "1//0gOtherClientToken") {
		t.Fatalf("another client's URL changed: %q", got)
	}
	arg := credEntry(t, claude, "wrapped")["args"].([]any)[1].(string)
	if strings.Contains(arg, "AIzaArgKey") || !strings.HasSuffix(arg, "&client_id="+googleClient) {
		t.Fatalf("argument URL after strip %q", arg)
	}
	for _, r := range records(t, home) {
		if r.Param == "access_token" && (r.Original != encodedToken || r.OriginalRaw != encodedRaw) {
			t.Fatalf("record keeps %q / %q", r.Original, r.OriginalRaw)
		}
	}
	if again, err := SyncCredentials(home, strips, time.Now()); err != nil || len(again) != 0 {
		t.Fatalf("re-scan acted: %+v %v", again, err)
	}
	if _, err := SyncCredentials(home, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(records(t, home)) != 0 {
		t.Fatal("records left after restore")
	}
	for _, s := range []string{"remote", "other", "wrapped"} {
		var orig map[string]any
		write(t, filepath.Join(home, "orig.json"), urlClaudeJSON)
		orig = credEntry(t, filepath.Join(home, "orig.json"), s)
		got := credEntry(t, claude, s)
		if mustJSON(orig) != mustJSON(got) {
			t.Fatalf("%s not restored exactly:\n%s\n%s", s, mustJSON(got), mustJSON(orig))
		}
	}
}

func TestURLQueryFingerprintMatchesTheDecodedValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "no-gcloud"))
	t.Setenv("XDG_CONFIG_HOME", "")
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, urlClaudeJSON)
	acts, err := SyncCredentials(home, []CredentialStrip{{TokenSHA256: sha256Hex(encodedToken)}}, time.Now())
	if err != nil || len(acts) != 1 || acts[0].Server != "remote" || !strings.HasPrefix(acts[0].Note, "url?access_token") {
		t.Fatalf("got %+v %v", acts, err)
	}
}

// A Codex URL: the literal one is edited in place, byte for byte; the one with
// a TOML escape in it cannot be, so a match there is refused.
func TestTOMLURLQueryInPlaceAndEscapedRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "no-gcloud"))
	t.Setenv("XDG_CONFIG_HOME", "")
	codex := filepath.Join(home, ".codex", "config.toml")
	body := `# codex
[mcp_servers.remote]
url = "https://mcp.example.test/mcp?client_id=` + googleClient + `&token=` + encodedRaw + `"   # comment
`
	write(t, codex, body)
	strips := []CredentialStrip{{ClientID: googleClient}}
	acts, err := SyncCredentials(home, strips, time.Now())
	if err != nil || len(kinds(acts, "credential_quarantined")) != 1 {
		t.Fatalf("got %+v %v", acts, err)
	}
	got := mustRead(t, codex)
	r := records(t, home)[0]
	if strings.Replace(got, r.Placeholder, r.OriginalRaw, 1) != body || r.OriginalRaw != encodedRaw {
		t.Fatalf("edit not confined to the parameter value:\n%s", got)
	}
	if _, err := SyncCredentials(home, nil, time.Now()); err != nil || mustRead(t, codex) != body {
		t.Fatalf("not restored byte for byte: %v\n%s", err, mustRead(t, codex))
	}

	escaped := "[mcp_servers.remote]\nurl = \"https://mcp.example.test/mcp?client_id=" + googleClient + "&token=\\u0041bc\"\n"
	write(t, codex, escaped)
	acts, err = SyncCredentials(home, strips, time.Now())
	if err != nil || len(acts) != 1 || acts[0].Kind != "credential_unhandled" || !strings.HasPrefix(acts[0].Note, "url?token: ") {
		t.Fatalf("want a refusal, got %+v %v", acts, err)
	}
	if mustRead(t, codex) != escaped || len(records(t, home)) != 0 {
		t.Fatal("a refused file was edited or a record was written")
	}
}
