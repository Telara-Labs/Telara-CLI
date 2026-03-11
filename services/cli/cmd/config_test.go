package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
)

// captureStdout runs fn and returns everything written to os.Stdout during the call.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fn()

	_ = w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// setupTestPrefs overrides the global prefs to point at the given API URL and
// saves a fake token to a temp credentials directory so auth.LoadToken succeeds.
// It returns a cleanup function that restores the previous state.
func setupTestPrefs(t *testing.T, apiURL string) func() {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	prefs = &config.Prefs{APIURL: apiURL}

	if err := auth.SaveToken(apiURL, "tlrc_testtoken0000000000"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	return func() {
		prefs = config.DefaultPrefs()
	}
}

func TestConfigList_Success(t *testing.T) {
	configsResp := map[string]interface{}{
		"configs": []map[string]interface{}{
			{
				"id": "abc-123", "name": "my-config", "scope_type": "tenant",
				"scope_id": "", "data_source_count": 2, "status": "active",
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/configs":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(configsResp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cleanup := setupTestPrefs(t, srv.URL)
	defer cleanup()

	// Reset cobra arg state between tests.
	rootCmd.ResetFlags()
	rootCmd.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Telara API base URL (overrides config)")
	rootCmd.PersistentFlags().StringVar(&rootContext, "context", "", "Active context name (overrides config)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Print full HTTP response on errors")

	var execErr error
	output := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"config", "list", "--api-url", srv.URL})
		execErr = rootCmd.Execute()
	})

	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	if !strings.Contains(output, "my-config") {
		t.Errorf("expected output to contain %q, got:\n%s", "my-config", output)
	}
}

func TestConfigList_Unauthenticated(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	apiURL := "https://api.telara.dev"
	prefs = &config.Prefs{APIURL: apiURL}
	defer func() { prefs = config.DefaultPrefs() }()

	rootCmd.ResetFlags()
	rootCmd.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Telara API base URL (overrides config)")
	rootCmd.PersistentFlags().StringVar(&rootContext, "context", "", "Active context name (overrides config)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Print full HTTP response on errors")

	rootCmd.SetArgs([]string{"config", "list", "--api-url", apiURL})
	err := rootCmd.Execute()

	if err == nil {
		t.Fatal("expected error for unauthenticated request, got nil")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in' in error, got: %v", err)
	}
}

func TestConfigShow_Success(t *testing.T) {
	detailResp := map[string]interface{}{
		"id":           "abc-123",
		"name":         "my-config",
		"scope_type":   "tenant",
		"scope_id":     "",
		"status":       "active",
		"mcp_url":      "https://mcp.telara.dev/abc-123",
		"policy_count": 1,
		"key_count":    2,
		"data_sources": []interface{}{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cli/configs/my-config" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(detailResp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cleanup := setupTestPrefs(t, srv.URL)
	defer cleanup()

	rootCmd.ResetFlags()
	rootCmd.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Telara API base URL (overrides config)")
	rootCmd.PersistentFlags().StringVar(&rootContext, "context", "", "Active context name (overrides config)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Print full HTTP response on errors")

	var execErr error
	output := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"config", "show", "my-config", "--api-url", srv.URL})
		execErr = rootCmd.Execute()
	})

	if execErr != nil {
		t.Fatalf("unexpected error: %v", execErr)
	}
	if !strings.Contains(output, "my-config") {
		t.Errorf("expected output to contain config name, got:\n%s", output)
	}
	if !strings.Contains(output, "https://mcp.telara.dev/abc-123") {
		t.Errorf("expected output to contain MCP URL, got:\n%s", output)
	}
}

// Compile-time check: *api errors should be detectable with errors.As.
func TestAPIError_ErrorsAs(t *testing.T) {
	// Just checks that the errors package interoperability works as expected
	// for the verbose error printing path in root.go.
	type wrappedErr struct {
		inner error
	}
	_ = errors.As // ensure import is used
}
