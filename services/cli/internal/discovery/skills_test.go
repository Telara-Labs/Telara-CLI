package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSkill lays down one skill directory: <root>/<dir>/SKILL.md plus any
// sibling asset files.
func writeSkill(t *testing.T, root, dir, content string, assets map[string]string) {
	t.Helper()
	skillDir := filepath.Join(root, dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	writeFixture(t, filepath.Join(skillDir, SkillFileName), content)
	for name, body := range assets {
		writeFixture(t, filepath.Join(skillDir, name), body)
	}
}

func skillsRoot(home string) string { return filepath.Join(home, ".claude", "skills") }

func TestScanSkills_ReadsFrontmatterAndHashesBody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	writeSkill(t, skillsRoot(home), "cold-email", `---
name: cold-email
description: Write B2B cold emails that get replies.
metadata:
  version: 1.1.0
---

# Cold Email Writing

Internal note: reach api.acme.internal for the lead list.
`, nil)

	results := ScanSkills()

	var global ConfigScanResult
	for _, r := range results {
		if r.Scope == ScopeGlobalSkills {
			global = r
		}
	}
	requireStatus(t, global, ScanOK)

	if len(global.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(global.Skills))
	}
	skill := global.Skills[0]
	if skill.SkillName != "cold-email" {
		t.Fatalf("unexpected skill name: %q", skill.SkillName)
	}
	if skill.Description != "Write B2B cold emails that get replies." {
		t.Fatalf("unexpected description: %q", skill.Description)
	}
	if !strings.HasPrefix(skill.ContentHash, "sha256:") || len(skill.ContentHash) != len("sha256:")+64 {
		t.Fatalf("expected a sha256 content hash, got %q", skill.ContentHash)
	}
}

// The body is the whole risk surface and the whole confidentiality problem. This
// test exists so that adding a body field can never pass review silently.
func TestScanSkills_NeverEmitsSkillBody(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	const secret = "SUPER-SECRET-INTERNAL-HOSTNAME-vault.corp.internal"
	writeSkill(t, skillsRoot(home), "leaky", `---
name: leaky
description: harmless looking
---

`+secret+`
`, nil)

	report := BuildReport(CollectorIdentity{}, "scan-1", time.Now(), time.Now(), ScanSkills())

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("skill body leaked into the discovery report")
	}
	if strings.Contains(string(encoded), "# ") {
		t.Fatal("markdown body content leaked into the discovery report")
	}
}

// A directory-name fallback matters: the malformed, hand-rolled skills are
// exactly the ones most worth surfacing, so unparseable frontmatter must not
// make a skill disappear.
func TestScanSkills_MissingFrontmatterFallsBackToDirectoryName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	writeSkill(t, skillsRoot(home), "no-frontmatter", "just a body, no delimiters\n", nil)

	results := ScanSkills()
	for _, r := range results {
		if r.Scope != ScopeGlobalSkills {
			continue
		}
		requireStatus(t, r, ScanOK)
		if len(r.Skills) != 1 {
			t.Fatalf("expected the malformed skill to still be reported, got %d", len(r.Skills))
		}
		if r.Skills[0].SkillName != "no-frontmatter" {
			t.Fatalf("expected directory-name fallback, got %q", r.Skills[0].SkillName)
		}
		if r.Skills[0].Description != "" {
			t.Fatalf("expected no description, got %q", r.Skills[0].Description)
		}
	}
}

// A nested `metadata:` block must not contribute a stray top-level key.
// A personally installed skill and an admin-deployed one are opposite findings.
// Mislabelling the user's own skills directory as managed_system asserts the
// wrong one, which is why this is pinned rather than left to PathClass's
// absolute-path fallback.
func TestScanSkills_PathClassDistinguishesUserFromManaged(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(project)

	writeSkill(t, skillsRoot(home), "mine", "---\nname: mine\n---\nbody\n", nil)
	writeSkill(t, filepath.Join(project, ".claude", "skills"), "repo", "---\nname: repo\n---\nbody\n", nil)

	for _, r := range ScanSkills() {
		switch r.Scope {
		case ScopeGlobalSkills:
			if r.PathClass != "user_global:claude_code_skills" {
				t.Fatalf("global skills path class = %q, want user_global:claude_code_skills", r.PathClass)
			}
		case ScopeProjectSkills:
			if r.PathClass != "project_local" {
				t.Fatalf("project skills path class = %q, want project_local", r.PathClass)
			}
		}
	}
}

func TestScanSkills_NestedKeysAreNotTopLevel(t *testing.T) {
	name, description := parseSkillFrontmatter(`---
description: real description
metadata:
  name: nested-should-be-ignored
---
body
`)
	if name != "" {
		t.Fatalf("nested name must not be read as top-level, got %q", name)
	}
	if description != "real description" {
		t.Fatalf("unexpected description: %q", description)
	}
}

func TestScanSkills_CountsAssetsAndFlagsExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	root := skillsRoot(home)
	writeSkill(t, root, "with-script", "---\nname: with-script\n---\nbody\n", map[string]string{
		"reference.md": "notes",
		"run.sh":       "#!/bin/sh\necho hi\n",
	})
	if err := os.Chmod(filepath.Join(root, "with-script", "run.sh"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	results := ScanSkills()
	for _, r := range results {
		if r.Scope != ScopeGlobalSkills {
			continue
		}
		if len(r.Skills) != 1 {
			t.Fatalf("expected 1 skill, got %d", len(r.Skills))
		}
		if r.Skills[0].ReferencedFileCount != 2 {
			t.Fatalf("expected 2 referenced files, got %d", r.Skills[0].ReferencedFileCount)
		}
		if !r.Skills[0].HasExecutable {
			t.Fatal("expected the executable bit to be detected")
		}
	}
}

// Absence is a complete answer and may retire previously-seen skills; an
// unreadable root is not and must not.
func TestScanSkills_AbsentRootIsCompleteUnreadableIsNot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	for _, r := range ScanSkills() {
		if r.Scope == ScopeGlobalSkills {
			requireStatus(t, r, ScanFileAbsent)
			if !AuthorizesTombstone(r.Status) {
				t.Fatal("an absent skills root is a complete answer and must authorize tombstoning")
			}
		}
	}

	root := skillsRoot(home)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial is not observable")
	}
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	for _, r := range ScanSkills() {
		if r.Scope == ScopeGlobalSkills {
			requireStatus(t, r, ScanPermissionDenied)
			if AuthorizesTombstone(r.Status) {
				t.Fatal("an unreadable root must never authorize tombstoning")
			}
		}
	}
}

// A directory with no SKILL.md is not a skill, and must not degrade the scope.
func TestScanSkills_DirectoryWithoutSkillFileIsNotASkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(filepath.Join(skillsRoot(home), "not-a-skill"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, r := range ScanSkills() {
		if r.Scope == ScopeGlobalSkills {
			requireStatus(t, r, ScanOK)
			if len(r.Skills) != 0 {
				t.Fatalf("expected no skills, got %d", len(r.Skills))
			}
		}
	}
}

// Skill scopes must not collide with MCP-config scopes. A shared key would
// double-count coverage and let an absent skills directory retire a real MCP
// configuration.
func TestBuildReport_SkillScopesDoNotCollideWithConfigScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	writeFixture(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"gh":{"command":"/usr/bin/npx","args":["-y","srv"]}}}`)
	writeSkill(t, skillsRoot(home), "s1", "---\nname: s1\ndescription: d\n---\nbody\n", nil)

	results := append(ScanAll(), ScanSkills()...)
	report := BuildReport(CollectorIdentity{}, "scan-1", time.Now(), time.Now(), results)

	seen := map[string]int{}
	for _, s := range report.Scopes {
		seen[s.SourceScope]++
	}
	for key, count := range seen {
		if count > 1 {
			t.Fatalf("scope key %q appeared %d times; scopes must be unique", key, count)
		}
	}

	var skills, configs int
	for _, a := range report.ResourceAssertions {
		switch a.ResourceKind {
		case ResourceKindAgentSkill:
			skills++
			if a.ContentHash == "" {
				t.Fatal("skill assertion must carry a content hash")
			}
			if a.CredentialClass != WireCredentialNone {
				t.Fatalf("a skill has no credential, got %q", a.CredentialClass)
			}
		case ResourceKindMCPClientConfiguration:
			configs++
			if a.ContentHash != "" {
				t.Fatal("an MCP config assertion must not carry a skill content hash")
			}
		}
	}
	if skills != 1 {
		t.Fatalf("expected 1 skill assertion, got %d", skills)
	}
	if configs != 1 {
		t.Fatalf("expected 1 config assertion, got %d", configs)
	}
}

// A skill loads from local disk and never traverses Telara, so the managed-host
// upgrade must not reach it.
func TestApplyManagedEndpoints_NeverUpgradesSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	writeSkill(t, skillsRoot(home), "s1", "---\nname: s1\n---\nbody\n", nil)

	report := BuildReport(CollectorIdentity{}, "scan-1", time.Now(), time.Now(), ScanSkills())
	ApplyManagedEndpoints(&report, []string{"https://telara.example.com"})

	for _, a := range report.ResourceAssertions {
		if a.ResourceKind == ResourceKindAgentSkill && a.ControlState != WireControlDiscoveredUnmediated {
			t.Fatalf("a skill must stay unmediated, got %q", a.ControlState)
		}
	}
}

// Re-scanning unchanged skills must produce a byte-identical report so
// downstream idempotency is exercisable.
func TestBuildReport_SkillReportIsDeterministic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	root := skillsRoot(home)
	writeSkill(t, root, "b-skill", "---\nname: b\n---\nbody\n", nil)
	writeSkill(t, root, "a-skill", "---\nname: a\n---\nbody\n", nil)

	at := time.Unix(1_700_000_000, 0).UTC()
	first, _ := json.Marshal(BuildReport(CollectorIdentity{}, "scan-1", at, at, ScanSkills()))
	second, _ := json.Marshal(BuildReport(CollectorIdentity{}, "scan-1", at, at, ScanSkills()))

	if string(first) != string(second) {
		t.Fatal("re-scanning unchanged skills must produce a byte-identical report")
	}
}
