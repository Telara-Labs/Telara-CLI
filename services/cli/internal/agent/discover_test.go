package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchToolNames_ReturnsWhatTheServerServes(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[
			{"name":"telara_knowledge_search"},{"name":"telara_tool_search"},{"name":"telara_archive_read"}]}}`))
	}))
	defer server.Close()

	// A connect URL ending in /sse must resolve to the Streamable HTTP endpoint.
	names, err := FetchToolNames(context.Background(), server.URL+"/v1/mcp/sse", "telara_mcp_key")
	if err != nil {
		t.Fatalf("FetchToolNames: %v", err)
	}
	if gotAuth != "Bearer telara_mcp_key" {
		t.Errorf("credential not forwarded, got %q", gotAuth)
	}
	if gotPath != "/v1/mcp" {
		t.Errorf("expected the /sse suffix to be trimmed, got %q", gotPath)
	}
	want := []string{"telara_archive_read", "telara_knowledge_search", "telara_tool_search"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted)", names, want)
		}
	}
}

// Setup must never fail because auto-approval could not be personalised.
func TestResolveToolNames_FallsBackWhenTheServerIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	names := ResolveToolNames(context.Background(), server.URL+"/v1/mcp", "telara_mcp_key")
	if len(names) != len(PlatformToolNames()) {
		t.Fatalf("expected the built-in fallback, got %d names", len(names))
	}
}

func TestResolveToolNames_FallsBackWithoutCredentials(t *testing.T) {
	if len(ResolveToolNames(context.Background(), "", "")) != len(PlatformToolNames()) {
		t.Fatal("missing credentials must fall back rather than return nothing")
	}
}

// An empty tool list is a server fault, not a legitimate answer — approving
// nothing would silently re-prompt the user for every call.
func TestFetchToolNames_RejectsAnEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer server.Close()

	if _, err := FetchToolNames(context.Background(), server.URL+"/v1/mcp", "k"); err == nil {
		t.Fatal("expected an error for an empty tool list")
	}
}
