package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests run a REAL MCP server (the official filesystem server) over real
// stdio. A mocked server would prove nothing about compatibility, which is the
// only question this spike exists to answer.
//
// They are skipped unless npx is present, and are opt-out via RELAY_SPIKE=0.

const spikeSecret = "estate-spike-secret-value-must-not-be-observed"

func requireMCPServer(t *testing.T) (string, []string, string) {
	t.Helper()
	if os.Getenv("RELAY_SPIKE") == "0" {
		t.Skip("RELAY_SPIKE=0")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx not available; the relay spike requires a real MCP server")
	}

	// A sandbox the filesystem server is allowed to touch. The file content
	// doubles as a canary: if it ever appears in an observation, the relay is
	// capturing payload content.
	sandbox := t.TempDir()
	if err := os.WriteFile(filepath.Join(sandbox, "probe.txt"), []byte(spikeSecret), 0o600); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}
	return npx, []string{"-y", "@modelcontextprotocol/server-filesystem@latest", sandbox}, sandbox
}

// session is a realistic MCP exchange: handshake, capability discovery, then a
// tool call that returns file content.
func session(sandbox string) string {
	readArgs, _ := json.Marshal(map[string]any{
		"name":      "read_text_file",
		"arguments": map[string]any{"path": filepath.Join(sandbox, "probe.txt")},
	})
	return strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"telara-relay-spike","version":"0.1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":%s}`, readArgs),
	}, "\n") + "\n"
}

// runDirect drives the server with no relay in the path — the control case.
func runDirect(t *testing.T, command string, args []string, input string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = strings.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !contains(err.Error(), "exit status") && !asExit(err, &exitErr) {
			t.Fatalf("direct run: %v", err)
		}
	}
	return out.String()
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func runRelayed(t *testing.T, command string, args []string, input string, observer Observer) (string, Stats) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	relay := &Relay{Command: command, Args: args, Observer: observer, ServerStderr: &bytes.Buffer{}}
	var out bytes.Buffer
	stats, err := relay.Run(ctx, strings.NewReader(input), &out)
	if err != nil && !contains(err.Error(), "server exited") {
		t.Fatalf("relayed run: %v", err)
	}
	return out.String(), stats
}

// TestRelayPreservesProtocolBytes is the spike's central question. If the relay
// alters a single byte of what the server produced, any client verifying or
// hashing raw frames breaks, and the mediation plane is not transparent.
func TestRelayPreservesProtocolBytes(t *testing.T) {
	command, args, sandbox := requireMCPServer(t)
	input := session(sandbox)

	direct := runDirect(t, command, args, input)
	relayed, stats := runRelayed(t, command, args, input, nil)

	if strings.TrimSpace(direct) == "" {
		t.Fatal("direct run produced no output; the server did not start")
	}
	if direct != relayed {
		t.Fatalf("relay altered the protocol stream.\n--- direct (%d bytes) ---\n%s\n--- relayed (%d bytes) ---\n%s",
			len(direct), truncate(direct), len(relayed), truncate(relayed))
	}
	if stats.ServerFrames == 0 {
		t.Fatal("relay forwarded no server frames")
	}
	t.Logf("byte-identical: %d bytes, %d client frames, %d server frames",
		len(direct), stats.ClientFrames, stats.ServerFrames)
}

// TestRelayObservesMetadataWithoutContent. The estate contract permits metadata
// only. The sandbox file content is a canary: the tool call returns it, so if
// the relay is over-capturing it will show up.
func TestRelayObservesMetadataWithoutContent(t *testing.T) {
	command, args, sandbox := requireMCPServer(t)

	var observed []Observation
	out, _ := runRelayed(t, command, args, session(sandbox), ObserverFunc(func(o Observation) {
		observed = append(observed, o)
	}))

	// The canary must genuinely have crossed the relay, or this test proves
	// nothing about redaction.
	if !contains(out, spikeSecret) {
		t.Fatalf("the tool call did not return the canary; the test cannot prove redaction. Output: %s", truncate(out))
	}

	var methods []string
	for _, o := range observed {
		rendered := fmt.Sprintf("%s|%s|%s|%s", o.Direction, o.Method, o.ID, o.ToolName)
		if contains(rendered, spikeSecret) {
			t.Fatalf("file content leaked into an observation: %s", rendered)
		}
		if contains(rendered, sandbox) {
			t.Fatalf("tool argument (path) leaked into an observation: %s", rendered)
		}
		if o.Method != "" {
			methods = append(methods, o.Method)
		}
	}

	for _, want := range []string{"initialize", "notifications/initialized", "tools/list", "tools/call"} {
		if !containsString(methods, want) {
			t.Fatalf("relay did not observe %q; saw %v", want, methods)
		}
	}

	// The invoked tool is governance signal and must be captured.
	var sawTool bool
	for _, o := range observed {
		if o.Method == "tools/call" && o.ToolName == "read_text_file" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatal("relay did not record which tool was invoked")
	}
	t.Logf("observed %d frames, methods: %v", len(observed), methods)
}

// TestRelayLatencyOverhead measures what the mediation plane costs. G1 requires
// a p95 overhead target; this establishes whether that target is plausible.
func TestRelayLatencyOverhead(t *testing.T) {
	command, args, sandbox := requireMCPServer(t)
	input := session(sandbox)

	const runs = 5
	directTimes := make([]time.Duration, 0, runs)
	relayTimes := make([]time.Duration, 0, runs)

	for i := 0; i < runs; i++ {
		start := time.Now()
		runDirect(t, command, args, input)
		directTimes = append(directTimes, time.Since(start))

		start = time.Now()
		runRelayed(t, command, args, input, nil)
		relayTimes = append(relayTimes, time.Since(start))
	}

	directMedian := median(directTimes)
	relayMedian := median(relayTimes)
	overhead := relayMedian - directMedian

	t.Logf("session wall-clock over %d runs — direct median %v, relayed median %v, overhead %v",
		runs, directMedian, relayMedian, overhead)

	// Most of a session's wall clock is npx resolving and booting node, which
	// dwarfs relay cost. The assertion is therefore deliberately loose: it is a
	// smoke check that the relay does not introduce a pathological stall, not a
	// precise per-call benchmark. Per-call latency is reported below.
	if overhead > 5*time.Second {
		t.Fatalf("relay added %v to a session; that is a pathological stall, not overhead", overhead)
	}
}

// TestRelayMeasuresPerRequestLatency proves the relay can attribute latency to
// individual requests, which is what a real p95 gate will be computed from.
func TestRelayMeasuresPerRequestLatency(t *testing.T) {
	command, args, sandbox := requireMCPServer(t)

	_, stats := runRelayed(t, command, args, session(sandbox), ObserverFunc(func(Observation) {}))
	if len(stats.Latencies) == 0 {
		t.Fatal("relay matched no request to a response; per-request latency is unmeasurable")
	}
	sorted := append([]time.Duration(nil), stats.Latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	t.Logf("matched %d request/response pairs; min %v median %v max %v",
		len(sorted), sorted[0], median(sorted), sorted[len(sorted)-1])
}

// TestRelaySurvivesServerFailure. A server that dies mid-session must not hang
// the relay, or a crashed MCP server would freeze the developer's client — the
// exact workflow breakage stop rule #1 is about.
func TestRelaySurvivesServerFailure(t *testing.T) {
	if os.Getenv("RELAY_SPIKE") == "0" {
		t.Skip("RELAY_SPIKE=0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A "server" that exits immediately without answering.
	relay := &Relay{Command: "sh", Args: []string{"-c", "exit 3"}, ServerStderr: &bytes.Buffer{}}
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() {
		_, err := relay.Run(ctx, strings.NewReader(session(t.TempDir())), &out)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a server exiting non-zero must be surfaced, not reported as success")
		}
		if !contains(err.Error(), "server exited") {
			t.Fatalf("unexpected failure mode: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("relay hung after the server died; this would freeze a developer's client")
	}
}

// TestRelayRejectsMissingCommand guards the obvious misconfiguration.
func TestRelayRejectsMissingCommand(t *testing.T) {
	relay := &Relay{}
	if _, err := relay.Run(context.Background(), strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("a relay with no server command must fail")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func median(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func truncate(s string) string {
	if len(s) <= 600 {
		return s
	}
	return s[:600] + "...[truncated]"
}
