package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// sampleEntry returns a non-trivial MCPEntry used across tests.
func sampleEntry() MCPEntry {
	return MCPEntry{
		Type:    "sse",
		URL:     "https://mcp.telara.dev/test",
		Headers: map[string]string{"Authorization": "Bearer tok"},
	}
}

// assertEntry fatals if the named server is absent or doesn't match want.
func assertEntry(t *testing.T, got map[string]MCPEntry, name string, want MCPEntry) {
	t.Helper()
	e, ok := got[name]
	if !ok {
		names := make([]string, 0, len(got))
		for k := range got {
			names = append(names, k)
		}
		t.Fatalf("server %q not found; present keys: %v", name, names)
	}
	if e.Type != want.Type || e.URL != want.URL {
		t.Fatalf("entry mismatch for %q: got %+v, want %+v", name, e, want)
	}
	for k, wv := range want.Headers {
		if gv := e.Headers[k]; gv != wv {
			t.Fatalf("header %q: got %q, want %q", k, gv, wv)
		}
	}
}

// ── Claude Code ──────────────────────────────────────────────────────────────

func TestClaudeCode_Global_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewClaudeCodeWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeGlobal, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".claude", "settings.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeGlobal)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeGlobal)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestClaudeCode_Project_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	w := NewClaudeCodeWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeProject, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".claude", "settings.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeProject)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeProject, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeProject)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestClaudeCode_Managed_ConfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewClaudeCodeWriter()
	path, err := w.ConfigPath(ScopeManaged)
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}

	var wantPath string
	switch runtime.GOOS {
	case "darwin":
		wantPath = "/Library/Application Support/ClaudeCode/managed-mcp.json"
	case "windows":
		wantPath = `C:\ProgramData\ClaudeCode\managed-mcp.json`
	default:
		wantPath = "/etc/claude-code/managed-mcp.json"
	}
	if path != wantPath {
		t.Errorf("managed config path = %q, want %q", path, wantPath)
	}
}

func TestClaudeCode_Global_WriteRemovePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewClaudeCodeWriter()
	if err := w.Write(ScopeGlobal, "telara", sampleEntry()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	pw, ok := w.(PermissionWriter)
	if !ok {
		t.Skip("claude-code writer does not implement PermissionWriter")
	}

	if err := pw.WritePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("WritePermissions: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	rule := permissionRule("telara")

	countRuleOccurrences := func() int {
		raw, _ := os.ReadFile(settingsPath)
		var cfg map[string]any
		json.Unmarshal(raw, &cfg)
		perms, _ := cfg["permissions"].(map[string]any)
		allow, _ := perms["allow"].([]any)
		n := 0
		for _, v := range allow {
			if s, ok := v.(string); ok && s == rule {
				n++
			}
		}
		return n
	}

	if n := countRuleOccurrences(); n != 1 {
		t.Fatalf("expected rule to appear once after WritePermissions, got %d", n)
	}

	// Idempotent — a second call must not duplicate.
	if err := pw.WritePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("WritePermissions (2nd): %v", err)
	}
	if n := countRuleOccurrences(); n != 1 {
		t.Fatalf("expected rule once after idempotent write, got %d", n)
	}

	if err := pw.RemovePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("RemovePermissions: %v", err)
	}
	if n := countRuleOccurrences(); n != 0 {
		t.Fatal("permission rule still present after RemovePermissions")
	}
}

func TestClaudeCode_Project_WritePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	w := NewClaudeCodeWriter()
	if err := w.Write(ScopeProject, "telara", sampleEntry()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	pw := w.(PermissionWriter)
	if err := pw.WritePermissions(ScopeProject, "telara"); err != nil {
		t.Fatalf("WritePermissions: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	raw, _ := os.ReadFile(settingsPath)
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	perms, _ := cfg["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	rule := permissionRule("telara")
	for _, v := range allow {
		if s, ok := v.(string); ok && s == rule {
			return // found
		}
	}
	t.Fatalf("permission rule %q not found in project settings", rule)
}

// ── Cursor ───────────────────────────────────────────────────────────────────

func TestCursor_Global_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewCursorWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeGlobal, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".cursor", "mcp.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeGlobal)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeGlobal)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestCursor_Project_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	w := NewCursorWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeProject, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".cursor", "mcp.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeProject)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeProject, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeProject)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestCursor_Managed_Unsupported(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewCursorWriter()
	if err := w.Write(ScopeManaged, "telara", sampleEntry()); err == nil {
		t.Fatal("expected error for ScopeManaged on cursor, got nil")
	}
}

func TestCursor_WriteRemovePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewCursorWriter()
	if err := w.Write(ScopeGlobal, "telara", sampleEntry()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	pw, ok := w.(PermissionWriter)
	if !ok {
		t.Skip("cursor writer does not implement PermissionWriter")
	}

	if err := pw.WritePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("WritePermissions: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".cursor", "mcp.json")
	readAutoApprove := func() []any {
		raw, _ := os.ReadFile(configPath)
		var cfg map[string]any
		json.Unmarshal(raw, &cfg)
		servers, _ := cfg["mcpServers"].(map[string]any)
		server, _ := servers["telara"].(map[string]any)
		list, _ := server["autoApprove"].([]any)
		return list
	}

	list := readAutoApprove()
	if len(list) == 0 {
		t.Fatal("autoApprove list is empty after WritePermissions")
	}
	toolSet := make(map[string]bool, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			toolSet[s] = true
		}
	}
	for _, name := range PlatformToolNames() {
		if !toolSet[name] {
			t.Errorf("tool %q missing from autoApprove", name)
		}
	}

	if err := pw.RemovePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("RemovePermissions: %v", err)
	}

	raw, _ := os.ReadFile(configPath)
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	servers, _ := cfg["mcpServers"].(map[string]any)
	server, _ := servers["telara"].(map[string]any)
	if _, exists := server["autoApprove"]; exists {
		t.Fatal("autoApprove field still present after RemovePermissions")
	}
}

// ── Windsurf ─────────────────────────────────────────────────────────────────

func TestWindsurf_Global_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewWindsurfWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeGlobal, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".codeium", "windsurf", "mcp_config.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeGlobal)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeGlobal)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestWindsurf_Project_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	w := NewWindsurfWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeProject, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".windsurf", "mcp_config.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeProject)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeProject, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeProject)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestWindsurf_Managed_Unsupported(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewWindsurfWriter()
	if err := w.Write(ScopeManaged, "telara", sampleEntry()); err == nil {
		t.Fatal("expected error for ScopeManaged on windsurf, got nil")
	}
}

func TestWindsurf_WriteRemovePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewWindsurfWriter()
	if err := w.Write(ScopeGlobal, "telara", sampleEntry()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	pw, ok := w.(PermissionWriter)
	if !ok {
		t.Skip("windsurf writer does not implement PermissionWriter")
	}

	if err := pw.WritePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("WritePermissions: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".codeium", "windsurf", "mcp_config.json")
	readAlwaysAllow := func() []any {
		raw, _ := os.ReadFile(configPath)
		var cfg map[string]any
		json.Unmarshal(raw, &cfg)
		servers, _ := cfg["mcpServers"].(map[string]any)
		server, _ := servers["telara"].(map[string]any)
		list, _ := server["alwaysAllow"].([]any)
		return list
	}

	list := readAlwaysAllow()
	if len(list) == 0 {
		t.Fatal("alwaysAllow list is empty after WritePermissions")
	}
	toolSet := make(map[string]bool, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			toolSet[s] = true
		}
	}
	for _, name := range PlatformToolNames() {
		if !toolSet[name] {
			t.Errorf("tool %q missing from alwaysAllow", name)
		}
	}

	if err := pw.RemovePermissions(ScopeGlobal, "telara"); err != nil {
		t.Fatalf("RemovePermissions: %v", err)
	}

	raw, _ := os.ReadFile(configPath)
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	servers, _ := cfg["mcpServers"].(map[string]any)
	server, _ := servers["telara"].(map[string]any)
	if _, exists := server["alwaysAllow"]; exists {
		t.Fatal("alwaysAllow field still present after RemovePermissions")
	}
}

// ── VS Code ───────────────────────────────────────────────────────────────────

func TestVSCode_Project_WriteReadRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	w := NewVSCodeWriter()
	entry := sampleEntry()

	if err := w.Write(ScopeProject, "telara", entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	wantPath := filepath.Join(tmpDir, ".vscode", "mcp.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config at %s: %v", wantPath, err)
	}

	got, err := w.Read(ScopeProject)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "telara", entry)

	if err := w.Remove(ScopeProject, "telara"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ = w.Read(ScopeProject)
	if _, ok := got["telara"]; ok {
		t.Fatal("entry still present after Remove")
	}
}

func TestVSCode_UsesServersKey(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	w := NewVSCodeWriter()
	if err := w.Write(ScopeProject, "telara", sampleEntry()); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(tmpDir, ".vscode", "mcp.json"))
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)

	if _, ok := cfg["servers"]; !ok {
		t.Fatalf("expected top-level key 'servers' in VS Code config, got keys: %v", cfg)
	}
	if _, ok := cfg["mcpServers"]; ok {
		t.Fatal("VS Code config must use 'servers', not 'mcpServers'")
	}
}

// ── Cross-client shared behaviour ────────────────────────────────────────────

func TestWrite_PreservesExistingEntries(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	w := NewClaudeCodeWriter()
	first := MCPEntry{Type: "sse", URL: "https://first.example.com"}
	second := MCPEntry{Type: "sse", URL: "https://second.example.com"}

	if err := w.Write(ScopeGlobal, "first", first); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if err := w.Write(ScopeGlobal, "second", second); err != nil {
		t.Fatalf("Write second: %v", err)
	}

	got, err := w.Read(ScopeGlobal)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEntry(t, got, "first", first)
	assertEntry(t, got, "second", second)
}

func TestRead_MissingFile_ReturnsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	writers := []AgentWriter{
		NewClaudeCodeWriter(),
		NewCursorWriter(),
		NewWindsurfWriter(),
		NewVSCodeWriter(),
	}
	for _, w := range writers {
		got, err := w.Read(ScopeProject)
		if err != nil {
			t.Errorf("%s: Read on missing file returned error: %v", w.Name(), err)
		}
		if len(got) != 0 {
			t.Errorf("%s: Read on missing file returned non-empty map: %v", w.Name(), got)
		}
	}
}

func TestRemove_MissingEntry_IsNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Chdir(tmpDir)

	writers := []AgentWriter{
		NewClaudeCodeWriter(),
		NewCursorWriter(),
		NewWindsurfWriter(),
		NewVSCodeWriter(),
	}
	for _, w := range writers {
		if err := w.Remove(ScopeProject, "nonexistent"); err != nil {
			t.Errorf("%s: Remove on missing entry returned error: %v", w.Name(), err)
		}
	}
}
