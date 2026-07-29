package relay

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealMCPClientDrivesTheRelay is the strongest compatibility evidence in
// this spike.
//
// Every other test drives the relay with JSON-RPC frames written by the same
// author as the relay, which can only demonstrate self-consistency. Stop rule #1
// of the commercial sequence is about real clients, so the claim needs a client
// implementation Telara did not write: one performing its own capability
// negotiation, protocol-version handling, schema validation and framing.
//
// The official @modelcontextprotocol/sdk client launches the relay binary
// exactly as it would launch a server, and the relay resolves the real server
// behind it. A failure here would mean the mediation plane is not transparent to
// real tooling, whatever the byte-level tests say.
//
// Opt-in: the SDK must be installed. Either set RELAY_SPIKE_CLIENT_DIR to a
// directory that already has node_modules/@modelcontextprotocol/sdk, or set
// RELAY_SPIKE_NPM_INSTALL=1 to let the test install into a temp dir.
func TestRealMCPClientDrivesTheRelay(t *testing.T) {
	if os.Getenv("RELAY_SPIKE") == "0" {
		t.Skip("RELAY_SPIKE=0")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx not available")
	}

	clientDir := resolveClientDir(t)
	if clientDir == "" {
		t.Skip("set RELAY_SPIKE_CLIENT_DIR to an SDK install, or RELAY_SPIKE_NPM_INSTALL=1")
	}

	// Build the relay binary the client will launch.
	relayBin := filepath.Join(t.TempDir(), "relay-spike")
	build := exec.Command("go", "build", "-o", relayBin, "./spikebin")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build relay binary: %v\n%s", err, out)
	}

	observations := filepath.Join(t.TempDir(), "observations.jsonl")
	const canary = "real-client-canary-value"

	cmd := exec.Command(node, "client.mjs")
	cmd.Dir = clientDir
	cmd.Env = append(os.Environ(),
		"RELAY_BIN="+relayBin,
		"RELAY_SPIKE_SERVER="+npx+" -y @modelcontextprotocol/server-everything@latest",
		"RELAY_SPIKE_OBSERVATIONS="+observations,
		"PROBE_TOOL=echo",
		`PROBE_TOOL_ARGS={"message":"`+canary+`"}`,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("real client failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), truncate(stderr.String()))
		}
	case <-time.After(300 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("real client hung against the relay")
	}

	var result struct {
		OK             bool     `json:"ok"`
		ServerName     string   `json:"serverName"`
		ServerVersion  string   `json:"serverVersion"`
		ToolCount      int      `json:"toolCount"`
		ToolNames      []string `json:"toolNames"`
		CalledTool     string   `json:"calledTool"`
		CallIsError    bool     `json:"callIsError"`
		CallText       string   `json:"callText"`
		CallTextLength int      `json:"callTextLength"`
		Error          string   `json:"error"`
	}
	line := lastJSONLine(stdout.String())
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		t.Fatalf("decode client result %q: %v", truncate(stdout.String()), err)
	}
	if !result.OK {
		t.Fatalf("real client reported failure: %s", result.Error)
	}

	// The SDK completed initialize, so version negotiation succeeded through the
	// relay rather than merely not crashing.
	if result.ServerName == "" {
		t.Fatal("client did not receive server identity; initialize did not complete")
	}
	if result.ToolCount == 0 {
		t.Fatal("client discovered no tools through the relay")
	}
	if result.CallIsError || !strings.Contains(result.CallText, canary) {
		t.Fatalf("tool call did not round-trip correctly: %+v", result)
	}
	t.Logf("real SDK client: server %s %s, %d tools, called %q successfully through the relay",
		result.ServerName, result.ServerVersion, result.ToolCount, result.CalledTool)

	// The canary genuinely crossed the relay in both directions, so its absence
	// from the observations is meaningful rather than incidental.
	recorded, err := os.ReadFile(observations)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	if bytes.Contains(recorded, []byte(canary)) {
		t.Fatalf("tool argument/result content leaked into observations:\n%s", truncate(string(recorded)))
	}

	var sawToolName, sawInitialize bool
	for _, entry := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		if entry == "" {
			continue
		}
		var obs struct {
			Method   string `json:"method"`
			ToolName string `json:"tool_name"`
		}
		if json.Unmarshal([]byte(entry), &obs) != nil {
			continue
		}
		if obs.Method == "initialize" {
			sawInitialize = true
		}
		if obs.ToolName == "echo" {
			sawToolName = true
		}
	}
	if !sawInitialize || !sawToolName {
		t.Fatalf("relay did not observe the real client's session correctly:\n%s", truncate(string(recorded)))
	}
	t.Logf("observations captured the session metadata with no argument or result content")
}

// resolveClientDir returns a directory containing client.mjs and an installed
// SDK, or "" if the test cannot run.
func resolveClientDir(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("testdata", "realclient", "client.mjs"))
	if err != nil {
		return ""
	}
	script, err := os.ReadFile(source)
	if err != nil {
		return ""
	}

	if dir := strings.TrimSpace(os.Getenv("RELAY_SPIKE_CLIENT_DIR")); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "node_modules", "@modelcontextprotocol", "sdk")); err == nil {
			// Always refresh the script so the committed fixture is what runs.
			if err := os.WriteFile(filepath.Join(dir, "client.mjs"), script, 0o600); err != nil {
				return ""
			}
			return dir
		}
		t.Logf("RELAY_SPIKE_CLIENT_DIR=%s has no @modelcontextprotocol/sdk", dir)
	}

	if os.Getenv("RELAY_SPIKE_NPM_INSTALL") != "1" {
		return ""
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"relay-spike-client","version":"1.0.0","type":"module","private":true}`), 0o600); err != nil {
		return ""
	}
	if err := os.WriteFile(filepath.Join(dir, "client.mjs"), script, 0o600); err != nil {
		return ""
	}
	install := exec.Command("npm", "install", "--silent", "@modelcontextprotocol/sdk")
	install.Dir = dir
	if out, err := install.CombinedOutput(); err != nil {
		t.Logf("npm install failed: %v\n%s", err, truncate(string(out)))
		return ""
	}
	return dir
}

func lastJSONLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "{") {
			return trimmed
		}
	}
	return ""
}
