package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

type installTestWriter struct {
	name      string
	path      string
	writeErr  error
	written   agent.MCPEntry
	writeCall int
}

func (w *installTestWriter) Name() string                           { return w.name }
func (w *installTestWriter) Detect() bool                           { return true }
func (w *installTestWriter) ConfigPath(agent.Scope) (string, error) { return w.path, nil }
func (w *installTestWriter) Write(_ agent.Scope, _ string, entry agent.MCPEntry) error {
	w.writeCall++
	w.written = entry
	return w.writeErr
}
func (w *installTestWriter) Read(agent.Scope) (map[string]agent.MCPEntry, error) {
	return map[string]agent.MCPEntry{}, nil
}
func (w *installTestWriter) Remove(agent.Scope, string) error { return nil }

func TestDryRunInstallNeverWrites(t *testing.T) {
	writer := &installTestWriter{name: "codex", path: "/tmp/config.toml"}
	results := dryRunInstall([]agent.AgentWriter{writer}, agent.ScopeGlobal)
	if writer.writeCall != 0 {
		t.Fatal("dry run wrote a configuration")
	}
	if len(results) != 1 || results[0].status != "DRY RUN" || results[0].detail != writer.path {
		t.Fatalf("unexpected results: %#v", results)
	}
}

// TestInstallUsesBaseKeyAndReportsEachClient: the user's own base config is the
// default binding for every client the installer writes (TENG-2306).
func TestInstallUsesBaseKeyAndReportsEachClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cli/configs/base/key" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_key":"telara_mcp_base_key","key_id":"key-77","mcp_config_id":"config-1","mcp_url":"https://api.telara.dev/v1/mcp","config_name":"Personal (u1)","scope_type":"user","scope_id":"u1"}`))
	}))
	defer server.Close()
	// Install caches the key for `telara mcp stdio` in the credential store;
	// keep that write in a throwaway home, never the developer's own.
	t.Setenv("HOME", t.TempDir())
	orig := prefs
	prefs = &config.Prefs{APIURL: server.URL}
	defer func() { prefs = orig }()

	writer := &installTestWriter{name: "codex", path: "/tmp/config.toml"}
	if _, err := installWritersWithCredential(context.Background(), api.NewClient(server.URL, "token"), []agent.AgentWriter{writer}, agent.ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if writer.written.Headers["Authorization"] != "Bearer telara_mcp_base_key" {
		t.Fatalf("client was not wired to the base credential: %#v", writer)
	}
	if key, err := auth.LoadMCPKey(server.URL); err != nil || key != "telara_mcp_base_key" {
		t.Fatalf("the key was not cached for telara mcp stdio: %q, %v", key, err)
	}
	if id, err := auth.LoadMCPKeyID(server.URL); err != nil || id != "key-77" {
		t.Fatalf("the key id was not cached for logout: %q, %v", id, err)
	}
}

func TestSelectedInstallConfigPrefersExplicitThenGlobal(t *testing.T) {
	state := &agent.WiredState{Global: &agent.WiredConfig{ConfigID: "connected-id", ConfigName: "Connected Engineering MCP"}}

	got, err := selectedInstallConfigForState("Personal", state)
	if err != nil || got != "Personal" {
		t.Fatalf("explicit config = %q, %v; want Personal, nil", got, err)
	}
	got, err = selectedInstallConfigForState("", state)
	if err != nil || got != "connected-id" {
		t.Fatalf("global config = %q, %v; want connected-id, nil", got, err)
	}
	got, err = selectedInstallConfigForState("", &agent.WiredState{})
	if err != nil || got != "" {
		t.Fatalf("first install config = %q, %v; want empty, nil", got, err)
	}
}

func TestInstallWritersWithConfigUsesSelectedProfileInsteadOfBaseKey(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/connected-id/deployments":
			_, _ = w.Write([]byte(`{"deployments":[{"id":"dep-1","scope_type":"user","scope_id":"u1"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/connected-id/keys":
			_, _ = w.Write([]byte(`{"id":"key-1","raw_key":"telara_mcp_connected","mcp_url":"` + server.URL + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"telara_knowledge_search"}]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/connected-id/keys":
			_, _ = w.Write([]byte(`{"keys":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/base/key":
			t.Fatal("selected profile install must not request a Personal base key")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	writer := &installTestWriter{name: "codex", path: "/tmp/config.toml"}
	results, err := installWritersWithConfig(context.Background(), api.NewClient(server.URL, "token"), []agent.AgentWriter{writer}, &api.MCPConfig{ID: "connected-id", Name: "Connected Engineering MCP"}, agent.ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if writer.written.Headers["Authorization"] != "Bearer telara_mcp_connected" {
		t.Fatalf("client was not wired to selected profile key: %#v", writer)
	}
	if len(results) != 1 || results[0].detail != "Connected Engineering MCP" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

// A partial client write still reports each result when the Personal key is available.
func TestInstallReportsPartialClientFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cli/configs/base/key" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_key":"telara_mcp_personal","key_id":"key-77","mcp_config_id":"config-1","scope_type":"user","scope_id":"u1","mcp_url":"https://api.telara.dev/v1/mcp"}`))
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	original := prefs
	prefs = &config.Prefs{APIURL: server.URL}
	defer func() { prefs = original }()
	good := &installTestWriter{name: "codex", path: "/tmp/config.toml"}
	bad := &installTestWriter{name: "cursor", path: "/tmp/mcp.json", writeErr: context.DeadlineExceeded}
	results, err := installWritersWithCredential(context.Background(), api.NewClient(server.URL, "token"), []agent.AgentWriter{good, bad}, agent.ScopeGlobal)
	if err == nil || good.writeCall != 1 || good.written.Headers["Authorization"] != "Bearer telara_mcp_personal" {
		t.Fatalf("expected partial failure after writing the Personal credential: %v", err)
	}
	if len(results) != 2 || results[0].status != "CONNECTED" || results[1].status != "FAILED" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestParseInstallScope(t *testing.T) {
	for input, want := range map[string]agent.Scope{"global": agent.ScopeGlobal, "MANAGED": agent.ScopeManaged} {
		got, err := parseInstallScope(input)
		if err != nil || got != want {
			t.Fatalf("parseInstallScope(%q) = %v, %v", input, got, err)
		}
	}
	_, err := parseInstallScope("project")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfirmInstall(t *testing.T) {
	writer := &installTestWriter{name: "codex"}
	var output bytes.Buffer
	if err := confirmInstall(strings.NewReader("yes\n"), &output, []agent.AgentWriter{writer}, agent.ScopeGlobal); err != nil {
		t.Fatalf("expected confirmation to pass: %v", err)
	}
	if !strings.Contains(output.String(), "codex") {
		t.Fatalf("confirmation did not name its target: %q", output.String())
	}
	if err := confirmInstall(strings.NewReader("no\n"), &bytes.Buffer{}, []agent.AgentWriter{writer}, agent.ScopeGlobal); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if err := confirmInstall(strings.NewReader(""), &bytes.Buffer{}, []agent.AgentWriter{writer}, agent.ScopeGlobal); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected non-interactive guard, got %v", err)
	}
}

func TestInstallDoesNotWriteClientsWhenPersonalBootstrapFails(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		http.Error(w, `{"error":"personal configuration is not ready"}`, http.StatusConflict)
	}))
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	original := prefs
	prefs = &config.Prefs{APIURL: server.URL}
	defer func() { prefs = original }()
	// A failed retry must not erase a previously stored credential either.
	const previousKey = "telara_mcp_existing"
	if err := auth.SaveMCPKey(server.URL, previousKey); err != nil {
		t.Fatal(err)
	}

	first := &installTestWriter{name: "codex", path: "/tmp/config.toml"}
	second := &installTestWriter{name: "cursor", path: "/tmp/mcp.json"}
	results, err := installWritersWithCredential(context.Background(), api.NewClient(server.URL, "token"), []agent.AgentWriter{first, second}, agent.ScopeGlobal)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected readiness failure, got %v", err)
	}
	if len(results) != 0 || first.writeCall != 0 || second.writeCall != 0 {
		t.Fatalf("failed bootstrap changed clients: results=%v first=%d second=%d", results, first.writeCall, second.writeCall)
	}
	if len(paths) != 1 || paths[0] != "POST /v1/cli/configs/base/key" {
		t.Fatalf("failed bootstrap requested another credential: %v", paths)
	}
	if key, err := auth.LoadMCPKey(server.URL); err != nil || key != previousKey {
		t.Fatalf("failed bootstrap did not preserve existing credential: %v", err)
	}
}
