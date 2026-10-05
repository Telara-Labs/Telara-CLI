package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// savedFolder writes a folder shaped like one `tap discover` saves.
func savedFolder(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".tap-primitive.json": marker,
		"SKILL.md":            "pointer the runner adds",
		"primitive.yaml":      "kind: Primitive\npublisher: com.acme\n",
		"main.sh":             "tap call jira.transition",
		"README.md":           "# close-ticket\n\nMove a ticket to done and comment on it.\n\nDetails.",
		"cases/one.json":      "{}",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadSavedPrimitiveSendsThePackageOnly(t *testing.T) {
	sp, err := readSavedPrimitive(savedFolder(t, `{"name":"com.acme/close-ticket","digest":"sha256:x","validation":"not_run"}`))
	if err != nil {
		t.Fatal(err)
	}
	if sp.Publisher != "com.acme" || sp.Name != "close-ticket" || sp.Summary != "Move a ticket to done and comment on it." {
		t.Fatalf("read %+v", sp)
	}
	var names []string
	for name := range sp.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	if want := []string{"README.md", "cases/one.json", "main.sh", "primitive.yaml"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v (no marker, no SKILL.md)", names, want)
	}
}

func TestReadSavedPrimitiveRefusesOtherFolders(t *testing.T) {
	if _, err := readSavedPrimitive(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a primitive saved by tap discover") {
		t.Errorf("plain folder: %v", err)
	}
	if _, err := readSavedPrimitive(savedFolder(t, `{"name":"close-ticket"}`)); err == nil || !strings.Contains(err.Error(), "not publisher/name") {
		t.Errorf("marker without a publisher: %v", err)
	}
}

func TestPublishRefusesAnUnknownAudience(t *testing.T) {
	rootCmd.SetArgs([]string{"tap", "publish", "--audience", "world", t.TempDir()})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	defer tapPublishCmd.Flags().Set("audience", "user")
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "--audience is user or tenant") {
		t.Fatalf("err = %v", err)
	}
}

func TestPublishGoesThroughTelaraSkillPublish(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		var msg struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &msg)
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-06-18"}}`, msg.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				t.Errorf("session header not carried")
			}
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name != "telara_skill_publish" {
				t.Errorf("tool = %s", p.Name)
			}
			got = p.Arguments
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"accepted com.acme/x@0.1.0\"}]}}\n\n", msg.ID)
		}
	}))
	defer srv.Close()
	m := &mcpCaller{endpoint: srv.URL, key: "k"}
	ctx := context.Background()
	if err := m.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	text, isErr, err := m.callTool(ctx, "telara_skill_publish", publishArgs("x", "desc", "com.acme", "user", "u-1", map[string][]byte{"primitive.yaml": []byte("kind: Primitive"), "main.sh": []byte("ls")}))
	if err != nil || isErr || text != "accepted com.acme/x@0.1.0" {
		t.Fatalf("text %q isErr %v err %v", text, isErr, err)
	}
	files, _ := got["files"].(map[string]any)
	if got["publisher"] != "com.acme" || got["target_scope_type"] != "user" || got["target_scope_id"] != "u-1" || files["main.sh"] != "ls" {
		t.Fatalf("arguments = %v", got)
	}
}

// The publish session trusts what every other request to Telara trusts: a
// gateway signed by the CA in TELARA_CA_CERT_PATH is reachable.
func TestTapPublishMCPUsesTelaraTransport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`)
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	bare := &mcpCaller{endpoint: srv.URL, key: "k"}
	if err := bare.initialize(context.Background()); err == nil {
		t.Fatal("the default client must not trust a self-signed gateway; the test proves nothing otherwise")
	}
	t.Setenv("TELARA_CA_CERT_PATH", ca)
	m := &mcpCaller{endpoint: srv.URL, key: "k", http: publishHTTPClient()}
	if err := m.initialize(context.Background()); err != nil {
		t.Fatalf("publish must trust TELARA_CA_CERT_PATH like every other request: %v", err)
	}
}
