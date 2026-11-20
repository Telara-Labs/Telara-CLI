package skillshare

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLiftedRemovalIsReinstatedOnTheNextRun(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	hash := installSkill(t, root, "withdrawn-skill", quarantineBody)
	installSkill(t, filepath.Join(root, "extra"), "nested-skill", "---\nname: nested-skill\n---\n\nNested.\n")
	t.Setenv("HOME", home)
	deny := []DeniedSkill{{ContentHash: hash, SkillName: "withdrawn-skill", Reason: "pulled"}}

	if _, err := Enforce(home, deny); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "withdrawn-skill", "SKILL.md")
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatal("precondition: the skill left the load path")
	}

	// Still denied: nothing comes back.
	if acts, err := Reinstate(home, deny); err != nil || len(acts) != 0 {
		t.Fatalf("still denied: %+v %v", acts, err)
	}

	// Lifted.
	acts, err := Reinstate(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Kind != "reinstated" {
		t.Fatalf("want one reinstated, got %+v", acts)
	}
	body, err := os.ReadFile(original)
	if err != nil || string(body) != quarantineBody {
		t.Fatalf("restored bytes differ: %v", err)
	}
	for _, f := range []string{"REMOVED.md", OriginFileName} {
		if _, err := os.Stat(filepath.Join(root, "withdrawn-skill", f)); !os.IsNotExist(err) {
			t.Errorf("%s must not travel back into the load path", f)
		}
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Fatalf("quarantine not emptied: %d", len(entries))
	}
}

func TestReinstateKeepsTheCopyWhenTheSkillReappeared(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	hash := installSkill(t, root, "withdrawn-skill", quarantineBody)
	t.Setenv("HOME", home)
	if _, err := Enforce(home, []DeniedSkill{{ContentHash: hash, SkillName: "withdrawn-skill"}}); err != nil {
		t.Fatal(err)
	}
	installSkill(t, root, "withdrawn-skill", "---\nname: withdrawn-skill\n---\n\nWritten again by hand.\n")
	acts, err := Reinstate(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Kind != "conflict" {
		t.Fatalf("want a conflict, got %+v", acts)
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 1 {
		t.Fatal("the quarantined copy must be kept")
	}
}

func TestReinstateLeavesATamperedCopy(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	hash := installSkill(t, root, "withdrawn-skill", quarantineBody)
	t.Setenv("HOME", home)
	acts, err := Enforce(home, []DeniedSkill{{ContentHash: hash, SkillName: "withdrawn-skill"}})
	if err != nil || len(acts) != 1 {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acts[0].To, "SKILL.md"), []byte("changed while quarantined"), 0o644); err != nil {
		t.Fatal(err)
	}
	back, err := Reinstate(home, nil)
	if err != nil || len(back) != 1 || back[0].Kind != "conflict" {
		t.Fatalf("a copy that no longer matches the lifted hash must stay: %+v %v", back, err)
	}
}

func TestOldQuarantinesWithoutAnOriginStayPut(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(QuarantineRoot(home), "legacy-20260101T000000Z")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if acts, err := Reinstate(home, nil); err != nil || len(acts) != 0 {
		t.Fatalf("%+v %v", acts, err)
	}
}
