package handoffrunner

// The run's loopback proxy (TENG-3693 G05). The upstream here is a bare local
// listener that records what reached it, standing in for "the MCP URL"; whether
// Telara accepts the proofs is proven against the real handoff authority, with the
// verifier the Gateway calls, in agent-service's TestPGLocalRunner*.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/dpop"
)

type seenRequest struct {
	method, path, auth, proof, cookie, body string
}

type upstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newUpstream(status int) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, seenRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("DPoP"), r.Header.Get("Cookie"), string(b)})
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("data: {\"ok\":true}\n\n"))
	}))
	return u
}

func (u *upstream) requests() []seenRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]seenRequest(nil), u.seen...)
}

func testKey(t *testing.T) *dpop.Key {
	t.Helper()
	k, err := dpop.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// checkProof reads a proof's claims and key. The signature itself is verified in
// package dpop and by the Gateway's verifier in agent-service.
func checkProof(t *testing.T, proof string, key *dpop.Key, method, htu, token string) {
	t.Helper()
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		t.Fatalf("no proof: %q", proof)
	}
	var header struct {
		JWK map[string]string `json:"jwk"`
	}
	var claims map[string]any
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	c, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(h, &header) != nil || json.Unmarshal(c, &claims) != nil {
		t.Fatalf("proof is not a JWT")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":%q,"y":%q}`, header.JWK["x"], header.JWK["y"])))
	ath := sha256.Sum256([]byte(token))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != key.Thumbprint() || claims["htm"] != method || claims["htu"] != htu ||
		claims["ath"] != base64.RawURLEncoding.EncodeToString(ath[:]) {
		t.Fatalf("proof %v by %v", claims, header.JWK)
	}
}

func TestProxyCarriesTheSessionAndAProofOnEveryCall(t *testing.T) {
	up := newUpstream(http.StatusOK)
	defer up.Close()
	key := testKey(t)
	p, err := startSessionProxy(up.URL+"/v1/mcp", "telara_handoff_t.i.s", key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if !strings.HasPrefix(p.URL(), "http://127.0.0.1:") || !strings.HasSuffix(p.URL(), "/v1/mcp") {
		t.Fatalf("proxy URL %s", p.URL())
	}
	for _, method := range []string{http.MethodPost, http.MethodPost, http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(method, p.URL()+"?x=1", bytes.NewReader([]byte(`{"jsonrpc":"2.0"}`)))
		req.Header.Set("Authorization", p.localAuthorization())
		req.Header.Set("Cookie", "session=leak")
		req.Header.Set("DPoP", "a-proof-codex-made-up")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
			t.Fatalf("%s through proxy: %d %s", method, resp.StatusCode, body)
		}
	}
	seen := up.requests()
	if len(seen) != 4 {
		t.Fatalf("upstream saw %d calls", len(seen))
	}
	jtis := map[string]bool{}
	for i, r := range seen {
		if r.path != "/v1/mcp" || r.auth != "DPoP telara_handoff_t.i.s" || r.cookie != "" {
			t.Fatalf("call %d reached Telara as %+v", i, r)
		}
		checkProof(t, r.proof, key, r.method, up.URL+"/v1/mcp", "telara_handoff_t.i.s")
		jtis[r.proof] = true
	}
	if len(jtis) != 4 {
		t.Fatal("a proof was reused; the Gateway refuses a replayed jti")
	}
}

// Another program on the machine cannot use the run's attachment: without the
// run's own token nothing is forwarded, so the session is never presented for it.
func TestProxyRefusesACallerWithoutTheRunsToken(t *testing.T) {
	up := newUpstream(http.StatusOK)
	defer up.Close()
	p, err := startSessionProxy(up.URL+"/v1/mcp", "telara_handoff_t.i.s", testKey(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"", "Bearer telara_handoff_t.i.s", "Bearer " + p.local + "x", "DPoP " + p.local} {
		req, _ := http.NewRequest(http.MethodPost, p.URL(), strings.NewReader("{}"))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("authorization %q got %d", auth, resp.StatusCode)
		}
	}
	other, _ := http.NewRequest(http.MethodPost, strings.TrimSuffix(p.URL(), "/v1/mcp")+"/v1/cli/handoff-runs", strings.NewReader("{}"))
	other.Header.Set("Authorization", p.localAuthorization())
	if resp, err := http.DefaultClient.Do(other); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a path other than the attachment: %v %v", resp, err)
	}
	p.close()
	req, _ := http.NewRequest(http.MethodPost, p.URL(), strings.NewReader("{}"))
	req.Header.Set("Authorization", p.localAuthorization())
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("the attachment outlived its run")
	}
	if n := len(up.requests()); n != 0 {
		t.Fatalf("%d refused calls reached Telara", n)
	}
}

// The control calls prove the same key: claim, input and report.
func TestControlCallsProveTheRunnersKey(t *testing.T) {
	up := newUpstream(http.StatusOK)
	defer up.Close()
	key := testKey(t)
	client := api.NewClient(up.URL, "tlrc_cli_token")
	client.Prover = key
	ctx := context.Background()
	_, _ = client.ClaimHandoffRun(ctx, "exec-1")
	_, _ = client.GetHandoffRunInput(ctx, "exec-1", "inc-1", api.HandoffRunAttachment{})
	_, _, _ = client.ReportHandoffRun(ctx, "exec-1", "inc-1", api.HandoffRunRunning)
	seen := up.requests()
	if len(seen) != 3 {
		t.Fatalf("upstream saw %d calls", len(seen))
	}
	for i, r := range seen {
		if r.auth != "Bearer tlrc_cli_token" {
			t.Fatalf("call %d auth %q", i, r.auth)
		}
		checkProof(t, r.proof, key, http.MethodPost, up.URL+r.path, "tlrc_cli_token")
	}
	bare := api.NewClient(up.URL, "tlrc_cli_token")
	if _, err := bare.ClaimHandoffRun(ctx, "exec-1"); err == nil || len(up.requests()) != 3 {
		t.Fatal("a claim without the runner's key was sent")
	}
}

func TestRunnerKeyIsMadeAtSetupOutsideTheJournalAndCodexHome(t *testing.T) {
	r := &Runner{Dir: t.TempDir()}
	if _, err := r.LoadKey(); err == nil {
		t.Fatal("a key before setup")
	}
	if err := r.PrepareHome(); err != nil {
		t.Fatal(err)
	}
	key, err := r.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(r.KeyPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v %v", info, err)
	}
	dir, _ := os.Stat(filepath.Dir(r.KeyPath()))
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("key directory mode %v", dir.Mode())
	}
	for _, inside := range []string{r.CodexHome(), r.journalDir()} {
		if rel, err := filepath.Rel(inside, r.KeyPath()); err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("the key is inside %s", inside)
		}
	}
	if err := r.PrepareHome(); err != nil {
		t.Fatal(err)
	}
	again, _ := r.LoadKey()
	if again.Thumbprint() != key.Thumbprint() {
		t.Fatal("setup replaced the runner's key")
	}
	if err := r.writeJournal(journalEntry{ExecutionID: "exec-1", IncarnationID: "inc-1", PID: 1}); err != nil {
		t.Fatal(err)
	}
	pemBytes, _ := os.ReadFile(r.KeyPath())
	journal, _ := os.ReadFile(r.journalPath("exec-1"))
	if bytes.Contains(journal, pemBytes[30:60]) || strings.Contains(string(journal), "PRIVATE") {
		t.Fatal("the journal holds the key")
	}
	// A run is never served without the key.
	r.Key = nil
	if err := r.Once(context.Background()); err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("served without a key: %v", err)
	}
}
