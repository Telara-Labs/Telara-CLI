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
	keysJSON    string
	server      *httptest.Server
}

func newLogoutGateway(t *testing.T, failRevoke bool) *logoutGateway {
	g := &logoutGateway{failRevoke: failRevoke, keysJSON: `{"keys":[{"id":"other","name":"x","key_prefix":"telara_mcp_zzzz"},{"id":"mine","name":"codex","key_prefix":"telara_mcp_abcd"}]}`}
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
			_, _ = w.Write([]byte(g.keysJSON))
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

// Every key of a tenant shares one prefix (telara_mcp_ + the tenant id), so a
// prefix match can pick someone else's key. The id stored at install is exact.
func TestLogoutRevokesTheKeyByStoredIDNotByPrefix(t *testing.T) {
	g := logoutFixture(t, false)
	g.keysJSON = `{"keys":[{"id":"colleague","name":"theirs","key_prefix":"telara_mcp_abcd"},{"id":"mine","name":"codex","key_prefix":"telara_mcp_abcd"}]}`
	if err := auth.SaveMCPKeyWithID(g.server.URL, "telara_mcp_abcdSECRETSECRET", "mine"); err != nil {
		t.Fatal(err)
	}

	if err := logoutCmd.RunE(logoutCmd, nil); err != nil {
		t.Fatal(err)
	}
	if len(g.keyDeletes) != 1 || g.keyDeletes[0] != "mine" {
		t.Fatalf("revoked keys = %v, want exactly [mine]", g.keyDeletes)
	}
}

// A cache entry from before the id was stored cannot say which key is ours.
// Two live keys share its prefix, so logout must not guess.
func TestLogoutLegacyCacheWithAmbiguousPrefixFailsLoudlyAndRevokesNothing(t *testing.T) {
	g := logoutFixture(t, false)
	g.keysJSON = `{"keys":[{"id":"colleague","name":"theirs","key_prefix":"telara_mcp_abcd"},{"id":"mine","name":"codex","key_prefix":"telara_mcp_abcd"}]}`

	err := logoutCmd.RunE(logoutCmd, nil)
	if err == nil {
		t.Fatal("logout succeeded although the cached key cannot be identified")
	}
	if !strings.Contains(err.Error(), "colleague") || !strings.Contains(err.Error(), "mine") {
		t.Fatalf("error does not name the candidate keys to revoke: %v", err)
	}
	if len(g.keyDeletes) != 0 || g.tokenDelete != 0 {
		t.Fatalf("revoked keys %v and token calls %d, want none", g.keyDeletes, g.tokenDelete)
	}
	if _, lerr := auth.LoadToken(g.server.URL); lerr != nil {
		t.Fatalf("the local credential was removed: %v", lerr)
	}
}

// Revoked keys do not count toward the ambiguity.
func TestLogoutLegacyCacheIgnoresRevokedPrefixMatches(t *testing.T) {
	g := logoutFixture(t, false)
	g.keysJSON = `{"keys":[{"id":"old","name":"x","key_prefix":"telara_mcp_abcd","revoked":true},{"id":"mine","name":"codex","key_prefix":"telara_mcp_abcd"}]}`

	if err := logoutCmd.RunE(logoutCmd, nil); err != nil {
		t.Fatal(err)
	}
	if len(g.keyDeletes) != 1 || g.keyDeletes[0] != "mine" {
		t.Fatalf("revoked keys = %v, want [mine]", g.keyDeletes)
	}
}
