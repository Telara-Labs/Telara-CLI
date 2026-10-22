package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

type logoutGateway struct {
	mu          sync.Mutex
	failRevoke  bool
	tokenDelete int
	keyDeletes  []string
	server      *httptest.Server
}

func newLogoutGateway(t *testing.T, failRevoke bool) *logoutGateway {
	g := &logoutGateway{failRevoke: failRevoke}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/auth/validate":
			_, _ = w.Write([]byte(`{"user_id":"u1","tenant_id":"t1"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/cli/auth/token":
			g.tokenDelete++
			if g.failRevoke {
				http.Error(w, `{"error":"unavailable"}`, http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs":
			_, _ = w.Write([]byte(`{"configurations":[{"id":"cfg-1","name":"Personal"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/cfg-1/keys":
			_, _ = w.Write([]byte(`{"keys":[{"id":"other","name":"x","key_prefix":"telara_mcp_zzzz"},{"id":"mine","name":"codex","key_prefix":"telara_mcp_abcd"}]}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/cli/keys/"):
			g.keyDeletes = append(g.keyDeletes, strings.TrimPrefix(r.URL.Path, "/v1/cli/keys/"))
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	t.Cleanup(g.server.Close)
	return g
}

func logoutFixture(t *testing.T, failRevoke bool) *logoutGateway {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	g := newLogoutGateway(t, failRevoke)
	orig := prefs
	prefs = &config.Prefs{APIURL: g.server.URL}
	t.Cleanup(func() { prefs = orig })
	if err := auth.SaveToken(g.server.URL, "tlrc_logout_token"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveMCPKey(g.server.URL, "telara_mcp_abcdSECRETSECRET"); err != nil {
		t.Fatal(err)
	}
	return g
}

// A revoke the server refused must not read as a clean logout. The token is
// still valid server-side, so the command fails and keeps the local credential
// that a retry needs.
func TestLogoutFailsLoudlyWhenTheServerRevokeFails(t *testing.T) {
	g := logoutFixture(t, true)

	err := logoutCmd.RunE(logoutCmd, nil)
	if err == nil {
		t.Fatal("logout exited 0 although the token is still live server-side")
	}
	if !strings.Contains(err.Error(), "still valid") {
		t.Fatalf("error does not say the token is still live: %v", err)
	}
	if _, lerr := auth.LoadToken(g.server.URL); lerr != nil {
		t.Fatalf("the local credential was deleted although the revoke failed: %v", lerr)
	}
}

// The MCP key logout deletes locally must be revoked server-side first.
func TestLogoutRevokesTheCachedMCPKeyAndTheToken(t *testing.T) {
	g := logoutFixture(t, false)

	if err := logoutCmd.RunE(logoutCmd, nil); err != nil {
		t.Fatal(err)
	}
	if g.tokenDelete != 1 {
		t.Fatalf("token revoke calls = %d, want 1", g.tokenDelete)
	}
	if len(g.keyDeletes) != 1 || g.keyDeletes[0] != "mine" {
		t.Fatalf("revoked keys = %v, want [mine]", g.keyDeletes)
	}
	if _, err := auth.LoadToken(g.server.URL); err != auth.ErrNoToken {
		t.Fatalf("token still stored after logout: %v", err)
	}
	if _, err := auth.LoadMCPKey(g.server.URL); err != auth.ErrNoMCPKey {
		t.Fatalf("MCP key still stored after logout: %v", err)
	}
}
