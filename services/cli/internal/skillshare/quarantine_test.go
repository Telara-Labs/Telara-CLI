package skillshare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const quarantineBody = "---\nname: withdrawn-skill\n---\n\nInstruction text an admin pulled.\n"

// installSkill puts a skill in a root the way install would, and returns its hash.
func installSkill(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return ContentHash(body)
}

// The core property: a withdrawn skill leaves the load path, is NOT destroyed,
// and the person is told what happened.
func TestQuarantineMovesTheSkillAndExplainsItself(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	hash := installSkill(t, root, "withdrawn-skill", quarantineBody)

	action, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{
		ContentHash: hash,
		SkillName:   "withdrawn-skill",
		Reason:      "leaked an internal hostname",
		DeniedBy:    "admin@example.com",
		DeniedAt:    "2026-09-05T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if action == nil {
		t.Fatal("expected an action")
	}

	// Out of the load path — the entire security goal.
	if _, err := os.Stat(filepath.Join(root, "withdrawn-skill", "SKILL.md")); !os.IsNotExist(err) {
		t.Error("the skill must no longer be where the agent loads it")
	}

	// NOT destroyed. This control also covers skills a user wrote themselves,
	// so deleting would destroy an employee's own work.
	moved, err := os.ReadFile(filepath.Join(action.To, "SKILL.md"))
	if err != nil {
		t.Fatalf("the original content must survive quarantine: %v", err)
	}
	if string(moved) != quarantineBody {
		t.Error("quarantined content must be byte-identical to what was installed")
	}

	// And the person can find out why, and what to do.
	note, err := os.ReadFile(filepath.Join(action.To, "REMOVED.md"))
	if err != nil {
		t.Fatalf("a removal with no note reads as the tool malfunctioning: %v", err)
	}
	for _, want := range []string{
		"leaked an internal hostname", // why
		"admin@example.com",           // who
		"request",                     // what to do about it
		"Re-creating the file",        // that re-creating will not work
	} {
		if !strings.Contains(string(note), want) {
			t.Errorf("removal note must mention %q:\n%s", want, note)
		}
	}
}

// A deny entry names a HASH, never a path. Enforcement must not be steerable
// outside the roots discovery itself defines — this moves real files.
func TestQuarantineRefusesAnythingOutsideTheSkillsRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../escape", "a/b", ".hidden", `..\windows`} {
		if _, err := quarantineOne(home, root, name, DeniedSkill{ContentHash: "sha256:x"}); err == nil {
			t.Errorf("name %q must be refused", name)
		}
	}

	// A symlink pointing out of the root must not turn enforcement into a way
	// to move an arbitrary directory.
	outside := filepath.Join(home, "somebody-elses-work")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sneaky")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := quarantineOne(home, root, "sneaky", DeniedSkill{ContentHash: "sha256:x"}); err == nil {
		t.Error("a skill directory resolving outside the root must be refused")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("the directory outside the root must be untouched")
	}
}

// Matching is by content hash. A skill nobody withdrew must never be moved,
// including one that merely shares a name with a withdrawn skill — a false
// positive here costs somebody their work.
func TestEnforceMatchesOnContentHashNotName(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")

	withdrawnHash := installSkill(t, root, "withdrawn-skill", quarantineBody)
	installSkill(t, root, "innocent-skill", "---\nname: innocent-skill\n---\n\nUnrelated.\n")
	// Same NAME as the withdrawn one is impossible in one root, so this covers
	// the other direction: different bytes under a name the deny entry carries.
	installSkill(t, root, "different-bytes", "---\nname: withdrawn-skill\n---\n\nEdited copy.\n")

	t.Setenv("HOME", home)
	actions, err := Enforce(home, []DeniedSkill{{
		ContentHash: withdrawnHash, SkillName: "withdrawn-skill", Reason: "pulled",
	}})
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("exactly the withdrawn skill should move, got %d: %+v", len(actions), actions)
	}
	if actions[0].SkillName != "withdrawn-skill" {
		t.Errorf("moved the wrong skill: %s", actions[0].SkillName)
	}
	for _, keep := range []string{"innocent-skill", "different-bytes"} {
		if _, err := os.Stat(filepath.Join(root, keep, "SKILL.md")); err != nil {
			t.Errorf("%s was not withdrawn and must not be touched: %v", keep, err)
		}
	}
}

func TestEnforceQuarantinesNestedSkillPackages(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".codex", "skills")
	hash := installSkill(t, root, ".system/nested-skill", quarantineBody)
	t.Setenv("HOME", home)

	actions, err := Enforce(home, []DeniedSkill{{ContentHash: hash, SkillName: "nested-skill"}})
	if err != nil {
		t.Fatalf("enforce nested skill: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected nested skill to move once, got %d: %+v", len(actions), actions)
	}
	if _, err := os.Stat(filepath.Join(root, ".system", "nested-skill", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("nested skill remained in its load path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(actions[0].To, "SKILL.md")); err != nil {
		t.Errorf("nested skill was not preserved in quarantine: %v", err)
	}
}

// An empty deny list must do nothing at all, without walking anything.
func TestEnforceIsANoOpWithNothingDenied(t *testing.T) {
	home := t.TempDir()
	actions, err := Enforce(home, nil)
	if err != nil || len(actions) != 0 {
		t.Fatalf("empty deny list must be a no-op, got %v / %v", actions, err)
	}
	actions, err = Enforce(home, []DeniedSkill{{ContentHash: "  "}})
	if err != nil || len(actions) != 0 {
		t.Fatalf("a blank hash must be ignored, got %v / %v", actions, err)
	}
}

// Quarantine lands OUTSIDE every skills root, or the agent would just discover
// it again in its new location.
func TestQuarantineRootIsOutsideTheLoadPath(t *testing.T) {
	home := t.TempDir()
	q := QuarantineRoot(home)
	if strings.Contains(q, filepath.Join(".claude", "skills")) {
		t.Errorf("quarantine must not live inside a skills root, got %s", q)
	}
	if !strings.HasPrefix(q, filepath.Join(home, ".telara")) {
		t.Errorf("quarantine should live under the CLI's own directory, got %s", q)
	}
}
