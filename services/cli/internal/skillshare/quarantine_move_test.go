package skillshare

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

var errXDev = &os.LinkError{Op: "rename", Err: syscall.EXDEV}

// crossDevice makes a rename into the quarantine root fail the way a rename
// across mounts does, so the fallback runs on any machine. failHold also fails
// the rename into the holding directory, to exercise the in-place path.
func crossDevice(t *testing.T, failHold bool) {
	t.Helper()
	old, oldHold := renameDir, holdRename
	renameDir = func(from, to string) error {
		if strings.Contains(to, filepath.Join(".telara", "quarantine")) {
			return errXDev
		}
		return os.Rename(from, to)
	}
	if failHold {
		holdRename = func(*os.Root, string, string) error { return errXDev }
	}
	t.Cleanup(func() {
		renameDir, holdRename = old, oldHold
		afterStageForTest, beforeCommitForTest, beforeEntryForTest, betweenOpenAndRenameForTest = nil, nil, nil, nil
	})
}

func writeFile(t *testing.T, p string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func fixtureSkill(t *testing.T, root string) (dir, outside string) {
	t.Helper()
	dir = filepath.Join(root, "withdrawn-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte(quarantineBody), 0o644)
	writeFile(t, filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh\necho synthetic\n"), 0o755)
	writeFile(t, filepath.Join(dir, "assets", "blob.bin"), []byte{0, 1, 2, 0xff, 0xfe, 0}, 0o600)
	writeFile(t, filepath.Join(dir, "empty.txt"), nil, 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(t.TempDir(), "outside-secret.txt")
	writeFile(t, outside, []byte("must never be copied"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "link-to-outside")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return dir, outside
}

// onlyHold returns the single held skill directory beside the skills root.
func onlyHold(t *testing.T, root string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(filepath.Dir(real), holdDir)
	containers, err := os.ReadDir(parent)
	if err != nil || len(containers) != 1 {
		t.Fatalf("want exactly one holding container in %s, got %v (%v)", parent, containers, err)
	}
	children, err := os.ReadDir(filepath.Join(parent, containers[0].Name()))
	if err != nil || len(children) != 1 {
		t.Fatalf("want exactly one held skill, got %v (%v)", children, err)
	}
	return filepath.Join(parent, containers[0].Name(), children[0].Name())
}

func assertTree(t *testing.T, at string, outside string) {
	t.Helper()
	for rel, want := range map[string]struct {
		data []byte
		mode os.FileMode
	}{
		"SKILL.md":        {[]byte(quarantineBody), 0o644},
		"scripts/run.sh":  {[]byte("#!/bin/sh\necho synthetic\n"), 0o755},
		"assets/blob.bin": {[]byte{0, 1, 2, 0xff, 0xfe, 0}, 0o600},
		"empty.txt":       {nil, 0o644},
	} {
		p := filepath.Join(at, rel)
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want.data) {
			t.Errorf("%s not preserved in %s: %v", rel, at, err)
		}
		if info, _ := os.Stat(p); info == nil || info.Mode().Perm() != want.mode {
			t.Errorf("%s mode not preserved in %s", rel, at)
		}
	}
	if info, err := os.Stat(filepath.Join(at, "emptydir")); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Errorf("empty directory not preserved in %s: %v", at, err)
	}
	link := filepath.Join(at, "link-to-outside")
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("a link must stay a link in %s: %v", at, err)
	}
	if target, _ := os.Readlink(link); target != outside {
		t.Errorf("link target changed to %q", target)
	}
}

func TestCrossFilesystemQuarantineKeepsEveryByteRetainsTheOriginalAndNeverFollowsLinks(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, outside := fixtureSkill(t, root)

	action, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody), Reason: "test"})
	if err != nil || action == nil {
		t.Fatalf("quarantine across filesystems: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("nothing may remain at the load path")
	}
	assertTree(t, action.To, outside)
	held := onlyHold(t, root)
	if action.Retained != held {
		t.Errorf("Retained = %q, want %q", action.Retained, held)
	}
	assertTree(t, held, outside) // the original is kept, not deleted
	for _, r := range []string{root} {
		if isWithin(r, held) {
			t.Errorf("the held original must be outside the skills root %s", r)
		}
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "must never be copied" {
		t.Error("the file outside the skill must be untouched")
	}
	if note, _ := os.ReadFile(filepath.Join(action.To, "REMOVED.md")); !strings.Contains(string(note), held) {
		t.Error("REMOVED.md must say where the original was kept")
	}
	if _, err := os.Stat(action.To + ".partial"); !os.IsNotExist(err) {
		t.Error("no staging directory may remain")
	}
}

// The race a delete step cannot close: after verification the person re-creates
// the skill and an editor atomically replaces a file. Nothing is unlinked, so
// both survive.
func TestCrossFilesystemQuarantineNeverUnlinksBytesWrittenAfterVerification(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	recreated := []byte("the person re-created the skill after verification\n")
	beforeCommitForTest = func() {
		writeFile(t, filepath.Join(dir, "SKILL.md"), recreated, 0o644)
		held := onlyHold(t, root)
		tmp := filepath.Join(held, "scripts", "run.sh.tmp")
		writeFile(t, tmp, []byte("atomic save"), 0o755)
		if err := os.Rename(tmp, filepath.Join(held, "scripts", "run.sh")); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)}); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); !bytes.Equal(got, recreated) {
		t.Error("the new file at the original path must be untouched")
	}
	held := onlyHold(t, root)
	if got, _ := os.ReadFile(filepath.Join(held, "scripts", "run.sh")); string(got) != "atomic save" {
		t.Error("the replaced file must still exist in the held original")
	}
}

func TestCrossFilesystemQuarantineAbortsAndKeepsTheOriginalWhenTheHeldTreeChangesDuringTheCopy(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	fixtureSkill(t, root)
	afterStageForTest = func() {
		writeFile(t, filepath.Join(onlyHold(t, root), "notes.md"), []byte("late write\n"), 0o644)
	}

	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("a change during the copy must fail the move, got %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(onlyHold(t, root), "notes.md")); string(got) != "late write\n" {
		t.Error("the late write must survive in the held original")
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Errorf("no staging or partial copy may remain, found %d entries", len(entries))
	}
}

// A directory swapped for a link to somewhere else between the copy and the
// check must be refused, not followed.
func TestCrossFilesystemQuarantineRefusesADirectorySwappedForAnEscapingLink(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	fixtureSkill(t, root)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "run.sh"), []byte("#!/bin/sh\necho synthetic\n"), 0o755)
	afterStageForTest = func() {
		scripts := filepath.Join(onlyHold(t, root), "scripts")
		if err := os.RemoveAll(scripts); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, scripts); err != nil {
			t.Fatal(err)
		}
	}

	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil {
		t.Fatal("a swapped directory must fail the move")
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Error("nothing may be committed")
	}
	if got, _ := os.ReadFile(filepath.Join(elsewhere, "run.sh")); string(got) != "#!/bin/sh\necho synthetic\n" {
		t.Error("the directory outside must be untouched")
	}
}

// With no way to hold the whole package, nothing is moved and nothing is
// reported as quarantined. The skill still loads, and the error says so.
func TestCrossFilesystemQuarantineFailsClosedWhenThePackageCannotBeHeld(t *testing.T) {
	crossDevice(t, true)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)

	action, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil || action != nil {
		t.Fatalf("an unholdable package must fail, got action=%v err=%v", action, err)
	}
	if !strings.Contains(err.Error(), "may still load") {
		t.Errorf("the error must say the skill may still load, got %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(got) != quarantineBody {
		t.Error("the source must be untouched")
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Error("no copy may be made")
	}
}

// A pre-existing holding path that is a link must not steer the rename. The
// outside directory receives nothing and the source is not displaced.
func TestCrossFilesystemQuarantineRefusesALinkedHoldingParent(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".claude", holdDir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil {
		t.Fatal("a linked holding parent must be refused")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("nothing may be written through the link, found %d entries", len(entries))
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(got) != quarantineBody {
		t.Error("the source must not be displaced")
	}
}

func TestCrossFilesystemQuarantineRefusesAHoldingParentThatIsAFile(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	writeFile(t, filepath.Join(home, ".claude", holdDir), []byte("not a directory"), 0o600)

	if _, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)}); err == nil {
		t.Fatal("a file in place of the holding parent must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Error("the source must be untouched")
	}
}

// The holding location may never overlap a known skills root, including through
// an alias, or the held copy would sit in a load path.
func TestCrossFilesystemQuarantineRefusesAHoldingParentInsideAKnownRoot(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(home, ".claude"), alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	old := knownSkillRoots
	knownSkillRoots = func() []string { return []string{alias} }
	t.Cleanup(func() { knownSkillRoots = old })

	if _, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)}); err == nil ||
		!strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("an aliased known root must block the hold, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Error("the source must be untouched")
	}
}

func TestMoveDirDoesNotFallBackOnOtherRenameErrors(t *testing.T) {
	old := renameDir
	t.Cleanup(func() { renameDir = old })
	renameDir = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EACCES} }
	base := t.TempDir()
	from := filepath.Join(base, "skills", "s")
	writeFile(t, filepath.Join(from, "SKILL.md"), []byte("x"), 0o644)
	_, err := moveDir(from, filepath.Join(t.TempDir(), "q"), filepath.Join(base, "skills"))
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("want the original error, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(base, holdDir)); !os.IsNotExist(e) {
		t.Error("nothing may be held or copied")
	}
}

// The swap lands between listing a directory and reading inside it. Reads go
// through the held tree's os.Root, so the escaping link is refused and nothing
// from outside is copied.
func TestCrossFilesystemQuarantineNeverReadsThroughADirectorySwappedMidCopy(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	fixtureSkill(t, root)
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "run.sh"), []byte("#!/bin/sh\necho outside\n"), 0o755)
	swapped := false
	beforeEntryForTest = func(rel string) {
		if rel == filepath.Join("scripts", "run.sh") && !swapped {
			swapped = true
			scripts := filepath.Join(onlyHold(t, root), "scripts")
			if err := os.RemoveAll(scripts); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, scripts); err != nil {
				t.Fatal(err)
			}
		}
	}

	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil {
		t.Fatal("a directory swapped for an escaping link mid-copy must fail the move")
	}
	// The refusal must come from the copy itself. A later comparison noticing the
	// swap would mean the outside file had already been read and copied.
	if !strings.Contains(err.Error(), "copy:") || strings.Contains(err.Error(), "changed while") {
		t.Errorf("the copy must refuse the escaping link, got %v", err)
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Error("nothing may be committed, and no partial copy may remain")
	}
}

// The holding parent is swapped for a link out of the common directory after the
// handles are open and before the rename. The rename runs through the common
// os.Root, so the link is refused and nothing is written outside.
func TestCrossFilesystemQuarantineRefusesAHoldingParentSwappedBeforeTheRename(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	outside := t.TempDir()
	betweenOpenAndRenameForTest = func() {
		hold := filepath.Join(home, ".claude", holdDir)
		// Give the link target a directory of the same name as the fresh
		// container, so an unconfined rename would succeed through the link.
		made, err := os.ReadDir(hold)
		if err != nil || len(made) != 1 {
			t.Fatalf("want one container before the swap, got %v (%v)", made, err)
		}
		if err := os.Mkdir(filepath.Join(outside, made[0].Name()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(hold); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, hold); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil {
		t.Fatal("a swapped holding parent must fail the move")
	}
	var leaked []string
	_ = filepath.WalkDir(outside, func(p string, d os.DirEntry, _ error) error {
		if p != outside && !d.IsDir() || (d.IsDir() && p != outside && filepath.Dir(p) != outside) {
			leaked = append(leaked, p)
		}
		return nil
	})
	if len(leaked) != 0 {
		t.Errorf("nothing may be written outside, found %v", leaked)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(got) != quarantineBody {
		t.Error("the source must still be where it was")
	}
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Error("no copy may be made")
	}
}

// The skill directory itself is replaced between opening it and renaming it. The
// handle no longer matches the directory at the path, so nothing is renamed and
// the replacement is left alone.
func TestCrossFilesystemQuarantineDoesNotRenameADirectoryThatIsNotTheOneOpened(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	replacement := []byte("a different skill the person put here\n")
	betweenOpenAndRenameForTest = func() {
		if err := os.Rename(dir, dir+".moved-away"); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "SKILL.md"), replacement, 0o644)
	}

	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil || !strings.Contains(err.Error(), "not the directory that was opened") {
		t.Fatalf("a replaced directory must not be renamed, got %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); !bytes.Equal(got, replacement) {
		t.Error("the replacement at the original path must be untouched")
	}
	if parent := filepath.Join(home, ".claude", holdDir); true {
		if entries, _ := os.ReadDir(parent); len(entries) != 0 {
			t.Errorf("no holding container may be left behind, found %d", len(entries))
		}
	}
}
