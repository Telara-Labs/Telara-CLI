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

func TestInstallUsesExplicitMasterKeyAndReportsEachClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cli/configs/master/key" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"master_key":"telara_mcp_tenant_key","mcp_url":"https://api.telara.dev/v1/mcp"}`))
	}))
	defer server.Close()

	good := &installTestWriter{name: "codex", path: "/tmp/config.toml"}
	bad := &installTestWriter{name: "cursor", path: "/tmp/mcp.json", writeErr: context.DeadlineExceeded}
	results, err := installWritersWithCredential(context.Background(), api.NewClient(server.URL, "token"), []agent.AgentWriter{good, bad}, agent.ScopeGlobal)
	if err == nil {
		t.Fatal("expected partial failure")
	}
	if good.writeCall != 1 || good.written.Headers["Authorization"] != "Bearer telara_mcp_tenant_key" {
		t.Fatalf("good writer did not receive master credential: %#v", good)
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

func TestOnboardingCredentialSkipsUndeployedFallbackConfigurations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/master/key":
			http.Error(w, `{"error":"master has no deployment"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/resolve":
			_, _ = w.Write([]byte(`{"managed":[],"user":[],"available":[{"id":"not-deployed","name":"Not deployed"},{"id":"ready","name":"Ready"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/not-deployed/deployments":
			_, _ = w.Write([]byte(`{"deployments":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/ready/deployments":
			_, _ = w.Write([]byte(`{"deployments":[{"id":"dep","scope_type":"tenant","scope_id":"tenant"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/ready/keys":
			_, _ = w.Write([]byte(`{"raw_key":"telara_mcp_fallback","mcp_url":"https://api.telara.dev/v1/mcp"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	key, endpoint, name, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err != nil {
		t.Fatal(err)
	}
	if key != "telara_mcp_fallback" || endpoint != "https://api.telara.dev/v1/mcp" || name != "Ready" {
		t.Fatalf("unexpected fallback: %q %q %q", key, endpoint, name)
	}
}
