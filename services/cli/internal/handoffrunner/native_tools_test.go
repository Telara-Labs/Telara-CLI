package handoffrunner

// Unit tests for TENG-3693 B11: the native-tool switches, the tool-free catalog,
// the tool-surface guard, the model proxy and the confinement profiles.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppServerStartsWithEveryNativeToolOff(t *testing.T) {
	args := launch{Catalog: "/h/cat.json", ModelURL: "http://127.0.0.1:9/m/s"}.args()
	if args[0] != "app-server" {
		t.Fatalf("args %v", args)
	}
	for _, f := range []string{"shell_tool", "unified_exec", "view_image", "apps", "plugins", "browser_use", "computer_use", "multi_agent", "code_mode", "hooks", "enable_request_compression"} {
		if !slices.Contains(args, f) || args[slices.Index(args, f)-1] != "--disable" {
			t.Fatalf("%s is not disabled: %v", f, args)
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{`model_catalog_json="/h/cat.json"`, `openai_base_url="http://127.0.0.1:9/m/s"`, `cli_auth_credentials_store="file"`, `mcp_oauth_credentials_store="file"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args lack %s: %v", want, args)
		}
	}
}

func TestThreadConfigTurnsNativeToolsOffAndHasOnlyTheGrant(t *testing.T) {
	cfg := threadConfig("http://127.0.0.1:1/v1/mcp", "Bearer x")
	if cfg["web_search"] != "disabled" {
		t.Fatalf("web search %v", cfg["web_search"])
	}
	tools := cfg["tools"].(map[string]any)
	if tools["web_search"] != false || tools["view_image"] != false {
		t.Fatalf("tools %v", tools)
	}
	features := cfg["features"].(map[string]any)
	for _, f := range disabledFeatures {
		if features[f] != false {
			t.Fatalf("feature %s is %v", f, features[f])
		}
	}
	if len(cfg["mcp_servers"].(map[string]any)) != 1 {
		t.Fatalf("servers %v", cfg["mcp_servers"])
	}
	// Codex's own approval prompt cannot be answered under approval never; the
	// grant session decides each call instead (B12b).
	if mode := cfg["mcp_servers"].(map[string]any)[AttachmentName].(map[string]any)["default_tools_approval_mode"]; mode != "approve" {
		t.Fatalf("grant session approval mode %v", mode)
	}
}

func TestToolFreeCatalogClearsModelOwnedTools(t *testing.T) {
	raw := []byte(`{"models":[{"slug":"a","apply_patch_tool_type":"freeform","tool_mode":"code","multi_agent_version":"v2","experimental_supported_tools":["x"],"supports_search_tool":true,"keep":1}]}`)
	out, err := neutralizeCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.Unmarshal(out, &got)
	m := got.Models[0]
	if m["apply_patch_tool_type"] != nil || m["tool_mode"] != nil || m["multi_agent_version"] != nil || len(m["experimental_supported_tools"].([]any)) != 0 || m["supports_search_tool"] != false || m["keep"] != float64(1) {
		t.Fatalf("catalog entry %v", m)
	}
	for _, bad := range []string{`{}`, `{"models":[]}`, `{"models":[1]}`, `not json`} {
		if _, err := neutralizeCatalog([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestToolFreeCatalogIsWrittenFromTheBinarysOwn(t *testing.T) {
	home := privateHome(t, false)
	path, err := writeToolFreeCatalog(fakeCodexBin, home)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"slug":"fake"`) || strings.Contains(string(b), "freeform") {
		t.Fatalf("catalog %s", b)
	}
}

func TestToolSurfaceReadsEveryFormCodexSends(t *testing.T) {
	body := map[string]any{
		"tools": []any{
			map[string]any{"type": "function", "name": "mcp__telara_handoff__reference_read"},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}}},
			map[string]any{"type": "web_search"},
		},
		"input": []any{map[string]any{"type": "additional_tools", "tools": []any{
			map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "function", "name": "list_mcp_resources"}}},
			map[string]any{"type": "custom", "name": "apply_patch"},
		}}},
	}
	var names []string
	for _, tr := range modelVisibleTools(body) {
		names = append(names, tr.String())
	}
	want := []string{"mcp__telara_handoff__reference_read", "collaboration.spawn_agent", "web_search", "functions.list_mcp_resources", "apply_patch"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
}

func TestToolSurfaceAllowsOnlyTheGrantAndMCPHelpers(t *testing.T) {
	grant := []string{"reference_read", "task_update"}
	ok := []toolRef{
		{name: "mcp__telara_handoff__reference_read"},
		{namespace: "mcp__telara_handoff__", name: "task_update"},
		{namespace: "functions", name: "list_mcp_resources"},
		{name: "tool_search"},
	}
	if err := checkToolSurface(ok, grant); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []toolRef{
		{name: "exec_command"}, {name: "apply_patch"}, {name: "view_image"}, {name: "web_search"},
		{namespace: "collaboration", name: "spawn_agent"},
		{name: "mcp__codex_apps__gmail_send"},
		{name: "mcp__telara_handoff__not_granted"},
		{namespace: "other", name: "list_mcp_resources"},
	} {
		err := checkToolSurface(append(ok, bad), grant)
		if err == nil || !strings.Contains(err.Error(), bad.String()) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// A grant session's tools deferred behind tool_search, or dropped, leave the run
// nothing to act with; the probe refuses it rather than let it end "completed"
// with no receipt (B12b).
func TestProbeRequiresTheGrantSessionsToolsOffered(t *testing.T) {
	grant := []string{"task_checkpoint"}
	deferred := []toolRef{{namespace: "functions", name: "list_mcp_resources"}, {name: "tool_search"}}
	if err := checkToolSurface(deferred, grant); err != nil {
		t.Fatal(err)
	}
	if err := checkGrantToolsOffered(deferred, grant); err == nil || !strings.Contains(err.Error(), "none of the grant session's 1 tools") {
		t.Fatalf("deferred grant tools passed: %v", err)
	}
	if err := checkGrantToolsOffered(append(deferred, toolRef{namespace: "mcp__telara_handoff", name: "task_checkpoint"}), grant); err != nil {
		t.Fatal(err)
	}
	if err := checkGrantToolsOffered(deferred, nil); err != nil {
		t.Fatalf("a grant with no tools: %v", err)
	}
}

func TestTurnOutcomeIgnoresAnotherThreadsTurn(t *testing.T) {
	m := map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "probe", "turn": map[string]any{"status": "completed"}}}
	if _, ok := turnOutcome(m, "run"); ok {
		t.Fatal("a probe turn ended the run's turn")
	}
	if s, ok := turnOutcome(m, "probe"); !ok || s != "completed" {
		t.Fatalf("%q %v", s, ok)
	}
}

func TestUpstreamIsFixedBySignIn(t *testing.T) {
	if u, _ := upstreamFor("apiKey"); u != apiKeyModelUpstream {
		t.Fatal(u)
	}
	if u, _ := upstreamFor("chatgpt"); u != chatGPTModelUpstream {
		t.Fatal(u)
	}
	if _, err := upstreamFor("amazonBedrock"); err == nil {
		t.Fatal("an unknown sign-in got an upstream")
	}
}

// recordingUpstream stands in for the model endpoint: it answers 200 and keeps
// what it was sent.
type recordingUpstream struct {
	mu   sync.Mutex
	seen []*http.Request
}

func (u *recordingUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.seen = append(u.seen, r)
	u.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {}\n\n")), Request: r}, nil
}

func (u *recordingUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.seen)
}

func postModel(t *testing.T, url, thread string, tools []any, header map[string]string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"model": "m", "tools": tools})
	req, _ := http.NewRequest(http.MethodPost, url+"/responses", bytes.NewReader(b))
	req.Header.Set("thread-id", thread)
	req.Header.Set("Authorization", "Bearer codex-own")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestModelProxyForwardsOnlyCheckedRequests(t *testing.T) {
	up := &recordingUpstream{}
	p, err := startModelProxy(up, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	grantTool := []any{map[string]any{"type": "function", "name": "mcp__telara_handoff__task_update"}}
	shell := append(grantTool, map[string]any{"type": "function", "name": "exec_command"})

	if code := postModel(t, p.URL(), "run", grantTool, nil); code != http.StatusForbidden {
		t.Fatalf("before arm: %d", code)
	}
	if err := p.arm([]string{"task_update"}, "https://api.openai.com/v1"); err != nil {
		t.Fatal(err)
	}
	if code := postModel(t, p.URL(), "run", shell, nil); code != http.StatusForbidden {
		t.Fatalf("shell offered: %d", code)
	}
	if code := postModel(t, p.URL(), "run", grantTool, map[string]string{"Content-Encoding": "zstd"}); code != http.StatusUnsupportedMediaType {
		t.Fatalf("encoded: %d", code)
	}
	if code := postModel(t, p.URL(), "run", grantTool, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}); code != http.StatusUpgradeRequired {
		t.Fatalf("upgrade: %d", code)
	}
	resp, _ := http.Post(strings.Replace(p.URL(), p.secret, "wrong", 1)+"/responses", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong secret: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if up.count() != 0 {
		t.Fatalf("forwarded %d refused requests", up.count())
	}
	if code := postModel(t, p.URL(), "run", grantTool, nil); code != http.StatusOK {
		t.Fatalf("clean request: %d", code)
	}
	if up.count() != 1 {
		t.Fatalf("forwarded %d", up.count())
	}
	got := up.seen[0]
	if got.URL.String() != "https://api.openai.com/v1/responses" || got.Header.Get("Authorization") != "Bearer codex-own" {
		t.Fatalf("forwarded to %s with %q", got.URL, got.Header.Get("Authorization"))
	}
	if err := p.arm(nil, "http://plain.example"); err == nil {
		t.Fatal("a plain-http upstream was accepted")
	}
}

func TestModelProxyAnswersAProbeItselfAndCountsOnlyItsModelRequest(t *testing.T) {
	up := &recordingUpstream{}
	p, err := startModelProxy(up, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	seen := p.expectProbe("probe-thread")
	// A bodiless request on the probe thread (such as a WebSocket warm-up that
	// fell through) is not the probe's tools.
	req, _ := http.NewRequest(http.MethodGet, p.URL()+"/responses", nil)
	req.Header.Set("thread-id", "probe-thread")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	select {
	case v := <-seen:
		t.Fatalf("a bodiless request was read as the probe: %v", v)
	default:
	}
	if code := postModel(t, p.URL(), "probe-thread", []any{map[string]any{"type": "function", "name": "exec_command"}}, nil); code != http.StatusOK {
		t.Fatalf("probe: %d", code)
	}
	select {
	case v := <-seen:
		if len(v) != 1 || v[0].name != "exec_command" {
			t.Fatalf("probe saw %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("probe tools not delivered")
	}
	if up.count() != 0 {
		t.Fatal("a probe request left the machine")
	}
}

func TestResolveBinaryRefusesAScriptLauncher(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	_ = os.WriteFile(script, []byte("#!/usr/bin/env node\n"), 0o700)
	if _, err := resolveBinary(script); err == nil || !strings.Contains(err.Error(), "script launcher") {
		t.Fatalf("script accepted: %v", err)
	}
	link := filepath.Join(dir, "link")
	_ = os.Symlink(fakeCodexBin, link)
	got, err := resolveBinary(link)
	real, _ := filepath.EvalSymlinks(fakeCodexBin)
	if err != nil || got != real {
		t.Fatalf("resolved %q %v, want %q", got, err, real)
	}
}

func TestBubblewrapArgsExposeOnlyTheRunsPaths(t *testing.T) {
	c := &confinement{Bin: "/opt/codex/bin/codex", Home: "/r/codex-home", Workdir: "/r/runs/x", Tmp: "/r/codex-home/tmp", Ports: []int{1}}
	args := bwrapArgs(c)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--die-with-parent", "--unshare-pid", "--clearenv", "--ro-bind /opt/codex/bin /opt/codex/bin", "--bind /r/codex-home /r/codex-home", "--bind /r/runs/x /r/runs/x", "--chdir /r/runs/x", "--setenv CODEX_HOME /r/codex-home", "--setenv HOME /r/codex-home", "--setenv TMPDIR /r/codex-home/tmp"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bwrap lacks %q: %s", want, joined)
		}
	}
	for _, never := range []string{"/usr/bin", " /bin", "/etc", "/home", "--bind / ", "--share-net"} {
		if strings.Contains(joined, never) {
			t.Fatalf("bwrap exposes %q: %s", never, joined)
		}
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != c.Bin {
		t.Fatalf("bwrap does not end with the binary: %v", args)
	}
}
