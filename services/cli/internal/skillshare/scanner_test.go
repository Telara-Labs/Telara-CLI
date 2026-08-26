package skillshare

import (
	"strings"
	"testing"
)

func TestLocalScannerFindsCredentialsWithoutEchoingThem(t *testing.T) {
	const token = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	findings := Scan("token: " + token)
	if len(findings) == 0 || findings[0].RuleID != "R-03" {
		t.Fatalf("GitHub token was not detected: %+v", findings)
	}
	for _, finding := range findings {
		if strings.Contains(finding.Excerpt, "QRSTUVWXYZ") {
			t.Fatalf("scanner excerpt leaked the credential: %q", finding.Excerpt)
		}
	}
}

func TestLocalScannerIgnoresPlaceholdersAndVersions(t *testing.T) {
	body := "api_key = <your-api-key-here>\ntoken: ${GITHUB_TOKEN}\npassword = changeme\nversion: 10.0.0\n"
	if findings := Scan(body); len(findings) != 0 {
		t.Fatalf("placeholders and versions must not be findings: %+v", findings)
	}
}

func TestLocalScannerVerdictIsCitableAndBlocksCriticalFinding(t *testing.T) {
	_, verdict := ScanAndAssess("token: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\nhost: api.acme.internal\n", DefaultPolicy())
	if !verdict.Blocked || verdict.Score != 210 || verdict.Threshold != 100 || verdict.PolicyVersion == "" {
		t.Fatalf("unexpected verdict: %+v", verdict)
	}
	lines := Explain(verdict)
	if len(lines) != 3 || !strings.Contains(lines[0], "R-03") || !strings.Contains(lines[0], "line 1") {
		t.Fatalf("verdict is not actionable: %v", lines)
	}
}

func TestLocalScannerHandlesLongLines(t *testing.T) {
	body := strings.Repeat("a", 200_000) + " ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if findings := Scan(body); len(findings) == 0 {
		t.Fatal("credential after 64KiB was not detected")
	}
}
