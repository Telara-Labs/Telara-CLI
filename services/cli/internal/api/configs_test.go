package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssueMasterKeyPostsExplicitIssuanceRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/cli/configs/master/key" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Fatalf("authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"name":"telara-install"`) {
			t.Fatalf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"master_key":"telara_mcp_tenant_abc","mcp_url":"https://api.telara.dev/v1/mcp"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "session-token")
	result, err := client.IssueMasterKey(context.Background(), "telara-install")
	if err != nil {
		t.Fatal(err)
	}
	if result.MasterKey != "telara_mcp_tenant_abc" || result.MCPURL != "https://api.telara.dev/v1/mcp" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestIssueMasterKeyRejectsMissingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mcp_url":"https://api.telara.dev/v1/mcp"}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "session-token").IssueMasterKey(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "did not include a key") {
		t.Fatalf("error = %v", err)
	}
}
