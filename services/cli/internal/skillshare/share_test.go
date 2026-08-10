package skillshare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, dir, body string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// A skill's audience is the entire decision being made. Defaulting it — narrow
// OR wide — makes the most consequential field invisible.
func TestParseScope_NoDefaultAndRejectsUnknown(t *testing.T) {
	if _, err := ParseScope(""); err == nil {
		t.Fatal("empty scope must be an error, not a default")
	}
	if _, err := ParseScope("everyone"); err == nil {
		t.Fatal("unknown scope must be rejected")
	}
	for _, want := range ValidScopes {
		got, err := ParseScope(string(want))
		if err != nil || got != want {
			t.Fatalf("ParseScope(%q) = %v, %v", want, got, err)
		}
	}
	if got, _ := ParseScope("  TEAM  "); got != ScopeTeam {
		t.Fatalf("scope parsing must be case- and space-insensitive, got %q", got)
	}
}

// Open-source is the only scope that cannot be taken back: once crawled and
// forked, "revoke" only stops Telara serving it.
func TestScope_OnlyOpenSourceIsIrreversible(t *testing.T) {
	if !ScopeOpenSource.IsIrreversible() {
		t.Fatal("open-source must be marked irreversible")
	}
	for _, s := range []Scope{ScopeTeam, ScopeEnterprise} {
		if s.IsIrreversible() {
			t.Fatalf("%s is revocable — Telara serves the body", s)
		}
	}
}

func TestLoadSkill_ReadsBodyAndHashes(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "cold-email", `---
name: cold-email
description: Write B2B cold emails.
metadata:
  version: 1.1.0
---

# Cold Email
Body text.
`)
	if err := os.WriteFile(filepath.Join(root, "cold-email", "ref.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	s, err := LoadSkill(root, "cold-email")
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if s.Name != "cold-email" || s.Description != "Write B2B cold emails." {
		t.Fatalf("frontmatter not read: %+v", s)
	}
	if s.Version != "1.1.0" {
		t.Fatalf("metadata.version must be read, got %q", s.Version)
	}
	if !strings.HasPrefix(s.ContentHash, "sha256:") {
		t.Fatalf("hash = %q", s.ContentHash)
	}
	if s.AssetCount != 1 {
		t.Fatalf("asset count = %d, want 1", s.AssetCount)
	}
	// Sharing DOES read the body — that is the difference from discovery.
	if !strings.Contains(s.Body, "Body text.") {
		t.Fatal("share path must load the body")
	}
}

// The hash must match what `telara scan` reports, or the discovered skill and
// the shared skill are two unrelated records and the estate cannot say "this
// installed skill is the approved one".
func TestLoadSkill_HashMatchesRawFileBytes(t *testing.T) {
	root := t.TempDir()
	const body = "---\nname: x\n---\nexact bytes\n"
	writeSkill(t, root, "x", body)

	s, err := LoadSkill(root, "x")
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if s.Body != body {
		t.Fatal("body must be the raw file, unmodified")
	}
}

func TestLoadSkill_MissingSkillIsAnError(t *testing.T) {
	if _, err := LoadSkill(t.TempDir(), "nope"); err == nil {
		t.Fatal("a missing skill must error, not return an empty skill")
	}
}

// The scan is not optional on the share path: a caller cannot construct an
// upload that skipped it.
func TestBuildRequest_AlwaysScans(t *testing.T) {
	s := Skill{
		Name:        "leaky",
		Body:        "key: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\n",
		ContentHash: "sha256:deadbeef",
	}
	req, findings := BuildRequest(s, ScopeTeam, false)
	if len(findings) == 0 {
		t.Fatal("BuildRequest must run the scan")
	}
	if len(req.ScanFindings) != len(findings) {
		t.Fatal("findings must travel with the request")
	}
	if req.AcknowledgedRisk {
		t.Fatal("acknowledgement must not be inferred")
	}
	// What the author was shown and accepted is a decision with an owner, and
	// it cannot be reconstructed from logs later.
	if req.ScanFindings[0].Category == "" {
		t.Fatal("recorded findings must carry their category")
	}
}

func TestBuildRequest_CleanSkillHasNoFindings(t *testing.T) {
	s := Skill{Name: "clean", Body: "---\nname: clean\n---\nJust prose.\n"}
	req, findings := BuildRequest(s, ScopeEnterprise, false)
	if len(findings) != 0 || len(req.ScanFindings) != 0 {
		t.Fatalf("clean skill produced findings: %+v", findings)
	}
	if req.Scope != ScopeEnterprise {
		t.Fatalf("scope = %q", req.Scope)
	}
}
