package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A config the writer cannot parse must survive untouched. This is the whole
// point: readJSONConfig used to return an empty map on a parse failure, and the
// caller's very next act is to marshal that map and rename it over the file —
// so an unparseable config was silently replaced with one containing only the
// telara entry. Fleet-wide, that is an unrecoverable event with no undo.
func TestWriteRefusesRatherThanReplacingAnUnparseableConfig(t *testing.T) {
	// JSONC. Not corruption — .vscode/mcp.json documents comment support, and
	// ~/.gemini/settings.json is routinely hand-edited the same way.
	const original = `{
  // the user's own comment
  "servers": { "existing": { "type": "http", "url": "https://example.invalid" } }
}`
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	err := writeEntry(path, "servers", "telara", MCPEntry{Type: "http", URL: "https://telara.invalid/v1/mcp"})
	if err == nil {
		t.Fatal("writeEntry silently accepted an unparseable config; it must refuse")
	}

	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != original {
		t.Fatalf("the user's config was modified despite the refusal:\n%s", after)
	}
}

// The same guarantee for Codex's TOML.
func TestTOMLWriteRefusesRatherThanReplacingAnUnparseableConfig(t *testing.T) {
	const original = "this is not valid toml ][\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := readTOMLConfig(path); err == nil {
		t.Fatal("readTOMLConfig returned an empty map for an unparseable file; it must return an error")
	}

	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatalf("TOML config was modified: %s", after)
	}
}

// Unrelated top-level keys must survive a successful write. ~/.claude.json is
// the case that matters — it carries Claude Code's project history alongside
// mcpServers, so a write that preserved only the servers map would be
// destructive even on the happy path.
func TestWritePreservesUnrelatedTopLevelKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	seed := map[string]any{
		"projects":     map[string]any{"/home/dev/repo": map[string]any{"history": []any{"one", "two"}}},
		"numStartups":  41,
		"mcpServers":   map[string]any{"other": map[string]any{"type": "http", "url": "https://other.invalid"}},
		"userSettings": map[string]any{"theme": "dark"},
	}
	raw, _ := json.Marshal(seed)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeEntry(path, "mcpServers", "telara", MCPEntry{Type: "http", URL: "https://telara.invalid/v1/mcp"}); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"projects", "numStartups", "userSettings"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("write dropped unrelated key %q — full file: %s", key, data)
		}
	}
	servers, _ := got["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("write dropped a pre-existing MCP server entry")
	}
	if _, ok := servers["telara"]; !ok {
		t.Fatal("write did not add the telara entry")
	}
}

// Even a correct write should be recoverable, because the writer reformats the
// whole file (MarshalIndent) and destroys comments and key order on the way.
func TestWriteLeavesABackupOfThePreviousContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	original := []byte(`{"servers":{"existing":{"type":"http","url":"https://example.invalid"}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeEntry(path, "servers", "telara", MCPEntry{Type: "http", URL: "https://telara.invalid/v1/mcp"}); err != nil {
		t.Fatal(err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written before overwrite: %v", err)
	}
	if string(backup) != string(original) {
		t.Fatalf("backup does not hold the previous contents:\n%s", backup)
	}
}

// A first-time install has nothing to preserve; it must not fail trying.
func TestWriteToAbsentConfigNeedsNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp.json")
	if err := writeEntry(path, "servers", "telara", MCPEntry{Type: "http", URL: "https://telara.invalid/v1/mcp"}); err != nil {
		t.Fatalf("first-time write failed: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("a backup was written for a file that did not previously exist")
	}
}
