package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// captureOnboardingWarnings redirects fallback warnings into a buffer for the
// duration of the test. Tests using it must not run in parallel.
func captureOnboardingWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := onboardingWarn
	onboardingWarn = &buf
	t.Cleanup(func() { onboardingWarn = prev })
	return &buf
}

func TestCredentialFallbackPermitted(t *testing.T) {
	permitted := []error{
		&api.APIError{StatusCode: http.StatusNotFound, Message: "route missing"},
		&api.APIError{StatusCode: http.StatusConflict, Message: "no base configuration"},
		&api.APIError{StatusCode: http.StatusNotImplemented, Message: "not implemented"},
		// Wrapped APIErrors must still be recognised.
		fmt.Errorf("issue base key: %w", &api.APIError{StatusCode: http.StatusNotFound}),
	}
	for _, err := range permitted {
		if !credentialFallbackPermitted(err) {
			t.Errorf("credentialFallbackPermitted(%v) = false, want true", err)
		}
	}

	denied := []error{
		nil,
		errors.New("dial tcp: connection refused"),
		context.DeadlineExceeded,
		fmt.Errorf("base key response did not include a key"),
		&api.APIError{StatusCode: http.StatusBadRequest},
		&api.APIError{StatusCode: http.StatusUnauthorized},
		&api.APIError{StatusCode: http.StatusForbidden},
		&api.APIError{StatusCode: http.StatusInternalServerError},
		&api.APIError{StatusCode: http.StatusBadGateway},
		&api.APIError{StatusCode: http.StatusServiceUnavailable},
		&api.APIError{StatusCode: http.StatusGatewayTimeout},
	}
	for _, err := range denied {
		if credentialFallbackPermitted(err) {
			t.Errorf("credentialFallbackPermitted(%v) = true, want false", err)
		}
	}
}

// TestOnboardingCredentialTransientBaseErrorFails verifies that a 5xx from the
// base-key route fails the whole flow with the base-key error instead of
// silently downgrading to the tenant master (TENG-2353 defect B).
func TestOnboardingCredentialTransientBaseErrorFails(t *testing.T) {
	warnings := captureOnboardingWarnings(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/base/key":
			http.Error(w, `{"error":"agent service unavailable"}`, http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected request after transient base error: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected"}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	_, _, _, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err == nil {
		t.Fatal("expected a transient base-key error to fail, got success")
	}
	if !strings.Contains(err.Error(), "agent service unavailable") {
		t.Fatalf("error should carry the base-key failure, got: %v", err)
	}
	if warnings.Len() != 0 {
		t.Fatalf("no fallback happened, so no warning expected, got: %q", warnings.String())
	}
}

// TestOnboardingCredentialTransientMasterErrorFails verifies that when the base
// route is definitively absent but the master route fails transiently, the flow
// fails rather than continuing to the any-config fallback.
func TestOnboardingCredentialTransientMasterErrorFails(t *testing.T) {
	captureOnboardingWarnings(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/base/key":
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/master/key":
			http.Error(w, `{"error":"upstream timeout"}`, http.StatusBadGateway)
		default:
			t.Errorf("unexpected request after transient master error: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected"}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	_, _, _, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err == nil {
		t.Fatal("expected a transient master-key error to fail, got success")
	}
	if !strings.Contains(err.Error(), "upstream timeout") {
		t.Fatalf("error should carry the master-key failure, got: %v", err)
	}
}

// TestOnboardingCredentialMasterFallbackWarns verifies that the master fallback
// still works when the base route is definitively unavailable, and that it is
// no longer silent: the warning names the MASTER binding and the base error.
func TestOnboardingCredentialMasterFallbackWarns(t *testing.T) {
	warnings := captureOnboardingWarnings(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/base/key":
			http.Error(w, `{"error":"no base configuration is available for this user"}`, http.StatusConflict)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/master/key":
			_, _ = w.Write([]byte(`{"master_key":"telara_mcp_master","mcp_url":"https://api.telara.dev/v1/mcp"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected"}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	key, endpoint, name, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err != nil {
		t.Fatal(err)
	}
	if key != "telara_mcp_master" || endpoint != "https://api.telara.dev/v1/mcp" || name != "Master" {
		t.Fatalf("unexpected master fallback result: %q %q %q", key, endpoint, name)
	}

	out := warnings.String()
	if !strings.Contains(out, "MASTER") {
		t.Fatalf("warning must name the MASTER binding, got: %q", out)
	}
	if !strings.Contains(out, "union of every") {
		t.Fatalf("warning must explain the policy-union consequence, got: %q", out)
	}
	if !strings.Contains(out, "no base configuration is available for this user") {
		t.Fatalf("warning must include the concrete base-key error, got: %q", out)
	}
}

// TestOnboardingCredentialAnyConfigFallbackWarns verifies the third fallback
// (first deployed configuration) also requires definitive unavailability of
// both prior routes and prints a warning naming the configuration bound.
func TestOnboardingCredentialAnyConfigFallbackWarns(t *testing.T) {
	warnings := captureOnboardingWarnings(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/base/key":
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/master/key":
			http.Error(w, `{"error":"master has no deployment"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/resolve":
			_, _ = w.Write([]byte(`{"managed":[],"user":[],"available":[{"id":"ready","name":"Ready"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/ready/deployments":
			_, _ = w.Write([]byte(`{"deployments":[{"id":"dep","scope_type":"tenant","scope_id":"tenant"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/ready/keys":
			_, _ = w.Write([]byte(`{"raw_key":"telara_mcp_fallback","mcp_url":"https://api.telara.dev/v1/mcp"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":"unexpected"}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	key, _, name, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err != nil {
		t.Fatal(err)
	}
	if key != "telara_mcp_fallback" || name != "Ready" {
		t.Fatalf("unexpected fallback result: %q %q", key, name)
	}

	out := warnings.String()
	if !strings.Contains(out, `"Ready"`) {
		t.Fatalf("warning must name the bound configuration, got: %q", out)
	}
	if !strings.Contains(out, "master has no deployment") {
		t.Fatalf("warning must include the master-key error, got: %q", out)
	}
}
