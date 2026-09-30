package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
)

// fakeRunner puts a `tap` on PATH that records its arguments.
func fakeRunner(t *testing.T) (record string) {
	t.Helper()
	bin := t.TempDir()
	record = filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + record + "\necho ---- >> " + record + "\n"
	if err := os.WriteFile(filepath.Join(bin, "tap"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return record
}

func TestPullConnectsTheRunnerWithThisTenantsTelemetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const api = "https://api.telara.dev"
	if err := auth.SaveMCPKey(api, "telara_mcp_t_abc"); err != nil {
		t.Fatal(err)
	}
	record := fakeRunner(t)
	connectRunner([]string{"claude-code", "cursor", "codex", "claude-code"}, api)

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the runner was not called: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "----")
	var got []string
	for _, c := range calls {
		if c = strings.TrimSpace(c); c != "" {
			got = append(got, strings.ReplaceAll(c, "\n", " "))
		}
	}
	if len(got) != 2 {
		t.Fatalf("want one install per runner client (claude, codex), got %d: %q", len(got), got)
	}
	for _, c := range got {
		for _, want := range []string{
			"--env OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
			"--env OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.telara.dev",
			"--env OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer telara_mcp_t_abc",
		} {
			if !strings.Contains(c, want) {
				t.Errorf("%q lacks %q", c, want)
			}
		}
	}
	if !strings.HasPrefix(got[0], "install --client claude") || !strings.HasPrefix(got[1], "install --client codex") {
		t.Errorf("clients: %q", got)
	}
}

func TestPullSaysWhyRunsWillNotReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	record := fakeRunner(t)
	// No MCP key stored: the runner is not called.
	connectRunner([]string{"claude-code"}, "https://api.telara.dev")
	if _, err := os.Stat(record); err == nil {
		t.Fatal("the runner was called without a key")
	}
	// No runner on PATH: nothing to call, and no failure.
	t.Setenv("PATH", t.TempDir())
	connectRunner([]string{"claude-code"}, "https://api.telara.dev")
	// --no-connect skips it entirely.
	_ = auth.SaveMCPKey("https://api.telara.dev", "k")
	record = fakeRunner(t)
	tapNoConnect = true
	defer func() { tapNoConnect = false }()
	connectRunner([]string{"claude-code"}, "https://api.telara.dev")
	if _, err := os.Stat(record); err == nil {
		t.Fatal("--no-connect still called the runner")
	}
}
