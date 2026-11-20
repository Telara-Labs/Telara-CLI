package mcpquarantine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const claudeJSON = `{
  "numStartups": 12,
  "mcpServers": {
    "github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": {"GITHUB_TOKEN": "ghp_secret"}},
    "Linear": {"type": "http", "url": "https://mcp.linear.app/mcp"},
    "notes": {"command": "notes-mcp"}
  },
  "projects": {"/tmp/x": {"mcpServers": {"github": {"command": "local"}}}}
}`

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func servers(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("file is no longer valid JSON: %v\n%s", err, data)
	}
	return doc.MCPServers
}

func TestQuarantineAndReinstateRoundTrip(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude.json")
	cursor := filepath.Join(home, ".cursor", "mcp.json")
	write(t, claude, claudeJSON)
	write(t, cursor, `{"mcpServers": {"linear": {"url": "https://mcp.linear.app/sse"}}}`)
	original, _ := os.ReadFile(claude)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

	actions, err := Sync(home, map[string]bool{"github": true, "linear": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 3 {
		t.Fatalf("want 3 quarantined (claude github, claude Linear, cursor linear), got %+v", actions)
	}
	got := servers(t, claude)
	if _, ok := got["github"]; ok {
		t.Fatal("github still configured")
	}
	if _, ok := got["Linear"]; ok {
		t.Fatal("Linear (case-insensitive match) still configured")
	}
	if _, ok := got["notes"]; !ok {
		t.Fatal("an unblocked server was removed")
	}
	if !strings.Contains(mustRead(t, claude), `"numStartups": 12`) || !strings.Contains(mustRead(t, claude), `"command": "local"`) {
		t.Fatal("unrelated content or the project-level entry changed")
	}
	if len(servers(t, cursor)) != 0 {
		t.Fatal("cursor linear still configured")
	}
	recs, _ := os.ReadDir(Root(home))
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d", len(recs))
	}
	for _, r := range recs {
		st, _ := os.Stat(filepath.Join(Root(home), r.Name()))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("record %s mode %v, want 0600 (it holds credentials)", r.Name(), st.Mode().Perm())
		}
	}
	if st, _ := os.Stat(claude); st.Mode().Perm() != 0o644 {
		t.Fatalf("client file mode changed to %v", st.Mode().Perm())
	}

	// Still blocked: a second run changes nothing.
	again, err := Sync(home, map[string]bool{"github": true, "linear": true}, now)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run: %+v %v", again, err)
	}

	// Lift linear only.
	lifted, err := Sync(home, map[string]bool{"github": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifted) != 2 || lifted[0].Kind != "reinstated" || lifted[1].Kind != "reinstated" {
		t.Fatalf("want two reinstated, got %+v", lifted)
	}
	if _, ok := servers(t, claude)["Linear"]; !ok {
		t.Fatal("Linear not put back under its original key")
	}
	if _, ok := servers(t, cursor)["linear"]; !ok {
		t.Fatal("cursor linear not put back")
	}

	// Lift everything: the file is the original, apart from key order of the
	// entries put back.
	if _, err := Sync(home, map[string]bool{}, now); err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	_ = json.Unmarshal(original, &before)
	_ = json.Unmarshal([]byte(mustRead(t, claude)), &after)
	if b, _ := json.Marshal(before); string(b) != mustJSON(after) {
		t.Fatalf("restored file differs:\n%s\n%s", b, mustJSON(after))
	}
	if recs, _ := os.ReadDir(Root(home)); len(recs) != 0 {
		t.Fatalf("records left after every reinstate: %d", len(recs))
	}
	if !strings.Contains(mustRead(t, claude), "ghp_secret") {
		t.Fatal("the reinstated entry lost its credential")
	}
}

func TestReinstateNeverOverwritesAServerAddedBackByHand(t *testing.T) {
	home := t.TempDir()
	cursor := filepath.Join(home, ".cursor", "mcp.json")
	write(t, cursor, `{"mcpServers": {"github": {"command": "old"}}}`)
	now := time.Now()
	if _, err := Sync(home, map[string]bool{"github": true}, now); err != nil {
		t.Fatal(err)
	}
	write(t, cursor, `{"mcpServers": {"github": {"command": "new-by-hand"}}}`)
	actions, err := Sync(home, map[string]bool{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Kind != "conflict" {
		t.Fatalf("want a conflict, got %+v", actions)
	}
	if !strings.Contains(mustRead(t, cursor), "new-by-hand") {
		t.Fatal("the hand-added server was overwritten")
	}
	if recs, _ := os.ReadDir(Root(home)); len(recs) != 1 {
		t.Fatal("the quarantined entry must be kept on a conflict")
	}
}

func TestInvalidClientFileIsReportedAndLeftAlone(t *testing.T) {
	home := t.TempDir()
	cursor := filepath.Join(home, ".cursor", "mcp.json")
	write(t, cursor, `{"mcpServers": {`)
	if _, err := Sync(home, map[string]bool{"github": true}, time.Now()); err == nil {
		t.Fatal("an unparseable file must be reported")
	}
	if mustRead(t, cursor) != `{"mcpServers": {` {
		t.Fatal("an unparseable file was rewritten")
	}
}

func TestParseBlocklist(t *testing.T) {
	got, err := ParseBlocklist(`["github", " linear ", ""]`)
	if err != nil || !got["github"] || !got["linear"] || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := ParseBlocklist(""); err != nil || len(got) != 0 {
		t.Fatal("empty setting is an empty blocklist")
	}
	if _, err := ParseBlocklist(`{"a":1}`); err == nil {
		t.Fatal("non-array accepted")
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
