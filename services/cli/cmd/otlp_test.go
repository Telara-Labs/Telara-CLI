package cmd

import (
	"strings"
	"testing"
)

func TestOTLPExportSnippet(t *testing.T) {
	t.Parallel()
	got := otlpExportSnippet("https://otlp.telara.dev", "telara_mcp_tenant_secret")
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.telara.dev",
		`Authorization=Bearer telara_mcp_tenant_secret`,
		"http/protobuf",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("snippet missing %q\n%s", want, got)
		}
	}
}

func TestOTLPEndpointFromAPIURL(t *testing.T) {
	t.Parallel()
	if got := otlpEndpointFromAPIURL("https://api.telara.dev"); got != "https://otlp.telara.dev" {
		t.Fatalf("api host: got %q", got)
	}
	if got := otlpEndpointFromAPIURL("https://api.telara.dev/"); got != "https://otlp.telara.dev" {
		t.Fatalf("trailing slash: got %q", got)
	}
	if got := otlpEndpointFromAPIURL("http://localhost:8080"); got != "http://localhost:8080" {
		t.Fatalf("local: got %q", got)
	}
}
