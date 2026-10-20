//go:build !windows

package skillshare

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCrossFilesystemQuarantineRefusesSpecialFilesAndKeepsTheSource(t *testing.T) {
	crossDevice(t, false)
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	dir, _ := fixtureSkill(t, root)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	_, err := quarantineOne(home, root, "withdrawn-skill", DeniedSkill{ContentHash: ContentHash(quarantineBody)})
	if err == nil || !strings.Contains(err.Error(), "named pipe") {
		t.Fatalf("a named pipe must abort the move, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(onlyHold(t, root), "SKILL.md")); e != nil {
		t.Error("the original must be kept intact in the hold")
	}
	_ = dir
	if entries, _ := os.ReadDir(QuarantineRoot(home)); len(entries) != 0 {
		t.Error("no partial copy may remain")
	}
}
