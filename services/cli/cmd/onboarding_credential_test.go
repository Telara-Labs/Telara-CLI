package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// A failed personal bootstrap must not request a broader credential, including
// when an older gateway lacks the route or the configuration is not ready.
func TestOnboardingCredentialFailsClosed(t *testing.T) {
	for status, guidance := range map[int]string{http.StatusNotFound: "unavailable on this gateway", http.StatusConflict: "not ready", http.StatusNotImplemented: "unavailable on this gateway", http.StatusUnauthorized: "sign in again", http.StatusServiceUnavailable: "check access and retry"} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				http.Error(w, `{"error":"personal configuration unavailable"}`, status)
			}))
			defer server.Close()
			binding, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
			if err == nil || !strings.Contains(err.Error(), "personal configuration unavailable") || !strings.Contains(err.Error(), guidance) {
				t.Fatalf("expected actionable base-key error, got %v", err)
			}
			if binding.RawKey != "" || len(requests) != 1 || requests[0] != "POST /v1/cli/configs/base/key" {
				t.Fatalf("failure requested an unexpected binding: requests=%v", requests)
			}
		})
	}
}

func TestOnboardingCredentialRejectsMalformedSuccessWithoutFallback(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base_key":""}`))
	}))
	defer server.Close()
	binding, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err == nil || !strings.Contains(err.Error(), "did not include a key") {
		t.Fatalf("expected malformed response error, got %v", err)
	}
	if binding.RawKey != "" || len(requests) != 1 || requests[0] != "POST /v1/cli/configs/base/key" {
		t.Fatalf("malformed response requested an unexpected binding: requests=%v", requests)
	}
}

func TestOnboardingCredentialRejectsIncompleteOrNonPersonalBindings(t *testing.T) {
	for name, body := range map[string]string{
		"missing key id":    `{"base_key":"secret","mcp_config_id":"cfg","scope_type":"user","scope_id":"u"}`,
		"missing config id": `{"base_key":"secret","key_id":"key","scope_type":"user","scope_id":"u"}`,
		"missing owner":     `{"base_key":"secret","key_id":"key","mcp_config_id":"cfg","scope_type":"user"}`,
		"tenant scope":      `{"base_key":"secret","key_id":"key","mcp_config_id":"cfg","scope_type":"tenant","scope_id":"tenant"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			binding, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
			if err == nil || binding.RawKey != "" || requests != 1 {
				t.Fatalf("incomplete binding returned key or fallback: err=%v requests=%d", err, requests)
			}
		})
	}
}
