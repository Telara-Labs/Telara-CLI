package handoffrunner

// The Codex driver against fakecodex, a test double for the external provider
// only. The endpoint it attaches to here is a bare local HTTP listener that
// answers or refuses, standing in for "an MCP server Codex can or cannot reach";
// the runner's exchange with Telara itself is proven against real handoff code in
// agent-service (TestPGLocalRunnerEndToEndWithFakeCodex).

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

var fakeCodexBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakecodex")
	if err != nil {
		panic(err)
	}
	fakeCodexBin = filepath.Join(dir, "fakecodex")
	build := exec.Command("go", "build", "-o", fakeCodexBin, "./fakecodex")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func privateHome(t *testing.T, signedIn bool) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "codex-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if signedIn {
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"test":"double"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func records(t *testing.T, home string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(home, "fake-codex.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func reachable(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
}

func TestCodexIsolatedHomeReportsNotSignedIn(t *testing.T) {
	c, err := startCodex(fakeCodexBin, privateHome(t, false), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if ok, err := c.signedIn(); err != nil || ok {
		t.Fatalf("signed in %v %v", ok, err)
	}
	if c.version != "0.0.0-fake" {
		t.Fatalf("version %q", c.version)
	}
}

func TestCodexThreadHasOnlyTheGrantSessionAndNothingIsSentBeforeIt(t *testing.T) {
	up := newUpstream(http.StatusOK)
	defer up.Close()
	key := testKey(t)
	proxy, err := startSessionProxy(up.URL+"/v1/mcp", "telara_handoff_t.i.s", key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	home := privateHome(t, true)
	work := t.TempDir()
	c, err := startCodex(fakeCodexBin, home, work)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := c.startThread(work, proxy.URL(), proxy.localAuthorization()); err != nil {
		t.Fatal(err)
	}
	// The attachment initialized through the proxy, which presented the session
	// with a proof by the runner's key. Codex itself never held the credential.
	seen := up.requests()
	if len(seen) != 1 || seen[0].auth != "DPoP telara_handoff_t.i.s" {
		t.Fatalf("upstream saw %+v", seen)
	}
	checkProof(t, seen[0].proof, key, "POST", up.URL+"/v1/mcp", "telara_handoff_t.i.s")
	var start map[string]any
	for _, r := range records(t, home) {
		if r["kind"] == "thread_start" {
			start = r
		}
		if r["kind"] == "turn_start" {
			t.Fatal("input before the attachment was proven")
		}
	}
	if start == nil {
		t.Fatal("no thread")
	}
	servers, _ := start["servers"].([]any)
	if len(servers) != 1 || servers[0] != AttachmentName || start["required_"+AttachmentName] != true || start["auth_grant_session_"+AttachmentName] != false {
		t.Fatalf("thread %v", start)
	}
	if start["sandbox"] != "workspace-write" || start["approval_policy"] != "never" || start["ephemeral"] != true || start["cwd"] != work {
		t.Fatalf("thread settings %v", start)
	}
}

func TestCodexRequiredAttachmentThatCannotInitializeFailsTheThread(t *testing.T) {
	mcp := reachable(http.StatusUnauthorized)
	defer mcp.Close()
	home := privateHome(t, true)
	c, err := startCodex(fakeCodexBin, home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := c.startThread(t.TempDir(), mcp.URL, "Bearer local"); err == nil {
		t.Fatal("a thread started without its required grant session")
	}
}

func TestCodexTurnEndsOnInterrupt(t *testing.T) {
	mcp := reachable(http.StatusOK)
	defer mcp.Close()
	home := privateHome(t, true)
	work := t.TempDir()
	c, err := startCodex(fakeCodexBin, home, work)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err := c.startThread(work, mcp.URL, "Bearer local"); err != nil {
		t.Fatal(err)
	}
	if err := c.startTurn("fake:hang"); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(300 * time.Millisecond); c.interrupt() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := c.waitTurn(ctx)
	if err != nil || status != "interrupted" {
		t.Fatalf("turn %q %v", status, err)
	}
}

func TestRunnerHomeRefusesStandingConfiguration(t *testing.T) {
	r := &Runner{Dir: t.TempDir()}
	if err := r.PrepareHome(); err != nil {
		t.Fatal(err)
	}
	if err := r.checkHome(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.CodexHome(), "config.toml"), []byte("[mcp_servers.telara]\nurl='https://example.invalid'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.checkHome(); err == nil {
		t.Fatal("a standing configuration was accepted")
	}
	_ = os.Remove(filepath.Join(r.CodexHome(), "config.toml"))
	if err := os.Chmod(r.CodexHome(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.checkHome(); err == nil {
		t.Fatal("a home others can read was accepted")
	}
}

func TestFirstTurnCarriesTheTaskAndNoBodies(t *testing.T) {
	text := firstTurn(&api.HandoffRunInput{BriefJSON: `{"objective":"x"}`, ReferenceIDs: []string{"r1"}}, "task-1")
	for _, want := range []string{"Your task id: task-1", `{"objective":"x"}`, "r1", AttachmentName} {
		if !strings.Contains(text, want) {
			t.Fatalf("first turn lacks %q: %s", want, text)
		}
	}
}

func TestThreadConfigKeepsTheAuthorizationInTheAttachmentOnly(t *testing.T) {
	cfg := threadConfig("http://127.0.0.1:1/v1/mcp", "Bearer local-token")
	b, _ := json.Marshal(cfg)
	if strings.Count(string(b), "local-token") != 1 {
		t.Fatalf("authorization placement %s", b)
	}
	servers := cfg["mcp_servers"].(map[string]any)
	if len(servers) != 1 {
		t.Fatalf("servers %v", servers)
	}
}

func TestThreadConfigDisablesCodexApps(t *testing.T) {
	cfg := threadConfig("http://127.0.0.1:1/v1/mcp", "Bearer x")
	f, _ := cfg["features"].(map[string]any)
	if v, ok := f["apps"].(bool); !ok || v {
		t.Fatalf("features.apps must be false, got %#v", cfg["features"])
	}
}

func TestGuardNamesUnexpectedServer(t *testing.T) {
	st := map[string]any{"data": []any{map[string]any{"name": AttachmentName}, map[string]any{"name": "codex_apps"}}}
	err := checkOnlyGrantServer(st)
	if err == nil || !strings.Contains(err.Error(), "codex_apps") {
		t.Fatalf("want error naming codex_apps, got %v", err)
	}
	if err := checkOnlyGrantServer(map[string]any{"data": []any{map[string]any{"name": AttachmentName}}}); err != nil {
		t.Fatal(err)
	}
}
