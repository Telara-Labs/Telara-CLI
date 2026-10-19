package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/tlstest"
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

// tools/list discovery trusts the configured private CA like every other CLI
// request (TENG-3499) and still rejects an untrusted CA.
func TestFetchToolNames_PrivateCATrust(t *testing.T) {
	ca := tlstest.NewCA(t, "telara-private-ca")
	srv := ca.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"telara_probe"}]}}`)
	}), "127.0.0.1")

	t.Setenv("TELARA_CA_CERT_PATH", "")
	if _, err := FetchToolNames(context.Background(), srv.URL, "k"); !tlstest.IsVerifyFailure(err) {
		t.Fatalf("without the CA want certificate verification failure, got %v", err)
	}
	t.Setenv("TELARA_CA_CERT_PATH", ca.WriteBundle(t))
	names, err := FetchToolNames(context.Background(), srv.URL, "k")
	if err != nil || len(names) != 1 || names[0] != "telara_probe" {
		t.Fatalf("names %v err %v", names, err)
	}
	t.Setenv("TELARA_CA_CERT_PATH", tlstest.NewCA(t, "other").WriteBundle(t))
	if _, err := FetchToolNames(context.Background(), srv.URL, "k"); err == nil {
		t.Fatal("an untrusted CA must be rejected")
	}
}
