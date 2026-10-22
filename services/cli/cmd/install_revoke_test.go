package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// keyGateway is an httptest gateway that keeps the keys minted for one config
// and records every revoke, speaking only the HTTP contract the CLI uses.
type keyGateway struct {
	mu      sync.Mutex
	keys    []api.APIKey
	revoked []string
	server  *httptest.Server
}

func newKeyGateway(t *testing.T, prior ...api.APIKey) *keyGateway {
	g := &keyGateway{keys: prior}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/cfg/deployments":
			_, _ = w.Write([]byte(`{"deployments":[{"id":"dep-1","scope_type":"user","scope_id":"u1"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli/configs/cfg/keys":
			var req api.GenerateKeyRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			id := fmt.Sprintf("key-%d", len(g.keys)+1)
			g.keys = append(g.keys, api.APIKey{ID: id, Name: req.Name})
			_, _ = w.Write([]byte(`{"id":"` + id + `","raw_key":"telara_mcp_` + id + `","mcp_url":"` + g.server.URL + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/cli/configs/cfg/keys":
			_ = json.NewEncoder(w).Encode(api.ListKeysResponse{Keys: g.keys})
		case r.Method == http.MethodDelete && len(r.URL.Path) > len("/v1/cli/keys/"):
			id := r.URL.Path[len("/v1/cli/keys/"):]
			g.revoked = append(g.revoked, id)
			for i := range g.keys {
				if g.keys[i].ID == id {
					g.keys[i].Revoked = true
				}
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *keyGateway) revokedIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.revoked...)
}

// A failed install must leave the previous key alone. Every client still holds
// it, because no client was rewritten.
func TestInstallDoesNotRevokeKeysStillInUse_AllWritersFail(t *testing.T) {
	name := toolKeyName("codex")
	g := newKeyGateway(t, api.APIKey{ID: "old-key", Name: name})
	bad := &installTestWriter{name: "codex", path: "/tmp/config.toml", writeErr: context.DeadlineExceeded}

	_, err := installWritersWithConfig(context.Background(), api.NewClient(g.server.URL, "token"), []agent.AgentWriter{bad}, &api.MCPConfig{ID: "cfg", Name: "Cfg"}, agent.ScopeGlobal)
	if err == nil {
		t.Fatal("expected the install to fail")
	}
	if got := g.revokedIDs(); len(got) != 0 {
		t.Fatalf("a failed install revoked %v, leaving every client with a dead key", got)
	}
}

// install --all shares one key across every client. A later single-client
// install must not revoke the key the other clients still carry.
func TestInstallDoesNotRevokeKeysStillInUse_SingleClientReinstall(t *testing.T) {
	g := newKeyGateway(t)
	client := api.NewClient(g.server.URL, "token")
	cfg := &api.MCPConfig{ID: "cfg", Name: "Cfg"}
	first := &installTestWriter{name: "claude-code", path: "/tmp/a.json"}
	second := &installTestWriter{name: "cursor", path: "/tmp/b.json"}

	if _, err := installWritersWithConfig(context.Background(), client, []agent.AgentWriter{first, second}, cfg, agent.ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	sharedKey := second.written.Headers["Authorization"]

	again := &installTestWriter{name: "claude-code", path: "/tmp/a.json"}
	if _, err := installWritersWithConfig(context.Background(), client, []agent.AgentWriter{again}, cfg, agent.ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if got := g.revokedIDs(); len(got) != 0 {
		t.Fatalf("single-client reinstall revoked %v, but cursor still carries %s", got, sharedKey)
	}
}

// Reinstalling the same client set still retires the previous key.
func TestInstallRevokesTheKeyItReplaced(t *testing.T) {
	g := newKeyGateway(t)
	client := api.NewClient(g.server.URL, "token")
	cfg := &api.MCPConfig{ID: "cfg", Name: "Cfg"}
	for i := 0; i < 2; i++ {
		w1 := &installTestWriter{name: "claude-code", path: "/tmp/a.json"}
		w2 := &installTestWriter{name: "cursor", path: "/tmp/b.json"}
		if _, err := installWritersWithConfig(context.Background(), client, []agent.AgentWriter{w1, w2}, cfg, agent.ScopeGlobal); err != nil {
			t.Fatal(err)
		}
	}
	if got := g.revokedIDs(); len(got) != 1 || got[0] != "key-1" {
		t.Fatalf("revoked = %v, want [key-1]", got)
	}
}
