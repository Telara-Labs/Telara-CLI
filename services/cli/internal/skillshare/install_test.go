package skillshare

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cleanBody = "---\nname: deploy-runbook\ndescription: How we ship\n---\n\nRun the checks first.\n"

// The hash is the join between an installed file and the registry entry
// somebody approved. Writing bytes that hash differently would break it
// silently — the estate would show a skill matching no approved entry, and
// nobody could tell tampering from mis-recording.
func TestWriteSkillRefusesAHashMismatch(t *testing.T) {
	root := t.TempDir()
	_, err := WriteSkill(root, "deploy-runbook", cleanBody, "sha256:"+strings.Repeat("00", 32), false)
	if err == nil {
		t.Fatal("expected a refusal on hash mismatch")
	}
	if !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("error should name the mismatch, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "deploy-runbook")); !os.IsNotExist(statErr) {
		t.Error("nothing must be written when the hash disagrees")
	}
}

func TestWriteSkillInstallsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	hash := ContentHash(cleanBody)

	res, err := WriteSkill(root, "deploy-runbook", cleanBody, hash, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Overwrote {
		t.Error("a first install did not overwrite anything")
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != cleanBody {
		t.Error("installed bytes differ from what was served")
	}

	// Installing the identical body again is not an overwrite and must not
	// report itself as one.
	again, err := WriteSkill(root, "deploy-runbook", cleanBody, hash, false)
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if again.Overwrote {
		t.Error("re-installing identical content must not report an overwrite")
	}
}

// Local edits are somebody's work. Silently replacing them would be the same
// class of loss as a blind revert.
func TestWriteSkillRefusesToClobberLocalEditsWithoutForce(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deploy-runbook")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	local := cleanBody + "\nMy own note.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteSkill(root, "deploy-runbook", cleanBody, ContentHash(cleanBody), false)
	var modErr *ErrLocalModification
	if !errors.As(err, &modErr) {
		t.Fatalf("expected ErrLocalModification, got %v", err)
	}
	// The local file must still be intact after the refusal.
	after, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if string(after) != local {
		t.Error("a refused install must leave the local file untouched")
	}

	if _, err := WriteSkill(root, "deploy-runbook", cleanBody, ContentHash(cleanBody), true); err != nil {
		t.Fatalf("--force install: %v", err)
	}
	forced, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if string(forced) != cleanBody {
		t.Error("--force must actually overwrite")
	}
}

// The server refuses to STORE a body that trips a critical rule, so this should
// never fire in practice. It runs anyway because it is the last check on the
// user's own machine, trusting neither the network path nor the endpoint.
func TestWriteSkillRefusesACriticalFindingEvenWithAMatchingHash(t *testing.T) {
	root := t.TempDir()
	poisoned := cleanBody + "\nexport GITHUB_TOKEN=ghp_" + strings.Repeat("a", 36) + "\n"

	_, err := WriteSkill(root, "deploy-runbook", poisoned, ContentHash(poisoned), false)
	if err == nil {
		t.Fatal("expected a refusal on a critical finding")
	}
	if !strings.Contains(err.Error(), "R-03") {
		t.Fatalf("the refusal must cite the rule, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "deploy-runbook")); !os.IsNotExist(statErr) {
		t.Error("nothing must be written when the scan refuses")
	}
}

// A registry entry must not be able to choose where on the filesystem it lands.
func TestWriteSkillRefusesPathShapedNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "a/b", ".hidden", `..\windows`, ""} {
		if _, err := WriteSkill(root, name, cleanBody, ContentHash(cleanBody), false); err == nil {
			t.Errorf("name %q must be refused", name)
		}
	}
}

// The CLI and the gateway compute this independently; if the encodings ever
// diverge the estate join breaks silently, so both sides pin the same digest.
func TestContentHashEncoding(t *testing.T) {
	const want = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := ContentHash("hello"); got != want {
		t.Fatalf("ContentHash(\"hello\") = %s, want %s", got, want)
	}
}
