package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
)

// tapNoConnect is --no-connect on telara tap pull.
var tapNoConnect bool

// runnerClient maps a telara client name to the name the tap runner's own
// installer takes. Clients the runner cannot serve are absent.
var runnerClient = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"gemini":      "gemini",
}

// connectRunner registers the tap runner with each client it serves and hands
// it the standard OpenTelemetry settings, so every run of a pulled primitive
// reports its outcome to this tenant (TENG-3042, doc 35 C4). It drives the
// runner's own `tap install --env`, which writes the client's configuration
// the way the client itself would. Only OTEL_* variables are passed (ruling
// 35). Any step that cannot happen is said and skipped: installing the
// primitive does not depend on it.
func connectRunner(clients []string, apiURL string) {
	if tapNoConnect {
		return
	}
	runner := findRunner()
	if runner == "" {
		fmt.Println("\nThe tap runner is not on this machine, so runs will not report to Telara. " +
			"Install it (https://github.com/Telara-Labs/TAP-Runtime), then run telara tap pull again.")
		return
	}
	key, err := auth.LoadMCPKey(apiURL)
	if err != nil || strings.TrimSpace(key) == "" {
		fmt.Println("\nNo Telara MCP key is stored on this machine (run telara install), so runs will not report to Telara.")
		return
	}
	env := runnerTelemetryEnv(apiURL, key)
	seen := map[string]bool{}
	for _, c := range clients {
		rc, ok := runnerClient[c]
		if !ok || seen[rc] {
			continue
		}
		seen[rc] = true
		args := []string{"install", "--client", rc}
		for _, kv := range env {
			args = append(args, "--env", kv)
		}
		cmd := exec.Command(runner, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if strings.Contains(msg, "flag provided but not defined") {
				msg = "this tap runner is too old to take --env; update it"
			}
			fmt.Printf("\nCould not connect the runner to %s, so its runs will not report: %s\n", c, msg)
			continue
		}
		fmt.Printf("\nConnected the tap runner to %s; its runs report to Telara.\n", c)
	}
}

// runnerTelemetryEnv is the OpenTelemetry configuration for this tenant: the
// same endpoint and header `telara otlp` prints.
func runnerTelemetryEnv(apiURL, key string) []string {
	return []string{
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + otlpEndpointFromAPIURL(apiURL),
		"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer " + key,
	}
}

// findRunner returns the tap runner's path. Releases before the rename are
// called tap-runtime.
func findRunner() string {
	for _, name := range []string{"tap", "tap-runtime"} {
		if p, err := exec.LookPath(name); err == nil {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}
