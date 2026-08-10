package cmd

import (
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

func critical() []skillshare.Finding {
	return []skillshare.Finding{{Line: 1, Category: "GitHub token", Severity: skillshare.SeverityCritical, Excerpt: "ghp_…89"}}
}
func warnOnly() []skillshare.Finding {
	return []skillshare.Finding{{Line: 1, Category: "internal hostname", Severity: skillshare.SeverityWarn, Excerpt: "api…al"}}
}

// The safety property of the whole feature: automation must not be able to push
// a credential into the registry. --yes is not an override; only --force is.
func TestShareGate_CriticalRefusesAndOnlyForceOverrides(t *testing.T) {
	err := shareGate(critical(), false)
	if err == nil {
		t.Fatal("a credential-grade finding must refuse the share")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the refusal must say how to override: %v", err)
	}
	if err := shareGate(critical(), true); err != nil {
		t.Fatalf("--force must override: %v", err)
	}
}

// Blocking on warn-level findings would train people to pass --force by reflex,
// which is exactly what makes the critical gate useless.
func TestShareGate_WarnFindingsDoNotBlock(t *testing.T) {
	if err := shareGate(warnOnly(), false); err != nil {
		t.Fatalf("warn-level findings must not block: %v", err)
	}
}

func TestShareGate_CleanScanPasses(t *testing.T) {
	if err := shareGate(nil, false); err != nil {
		t.Fatalf("a clean scan must pass: %v", err)
	}
}

// Displayed excerpts get pasted into terminals and CI logs, so the command layer
// must not widen what the scanner deliberately truncated.
func TestShortHash_IsTruncated(t *testing.T) {
	full := "sha256:95f710a596c488b0e3ecc51bb5d5b1db8fa589251bbf8ad0f58169797eca89ab"
	got := shortHash(full)
	if len(got) != 12 || strings.Contains(got, "sha256:") {
		t.Fatalf("shortHash = %q", got)
	}
	if strings.Contains(full, got) == false {
		t.Fatal("short hash must be a prefix of the real hash")
	}
}

func TestPluraliseFindings(t *testing.T) {
	for in, want := range map[int]string{0: "clean.", 1: "1 finding", 3: "3 findings"} {
		if got := pluraliseFindings(in); got != want {
			t.Fatalf("pluraliseFindings(%d) = %q, want %q", in, got, want)
		}
	}
}
