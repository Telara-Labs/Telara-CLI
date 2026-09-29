package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

// A folder written by `telara tap pull` carries a marker naming the promoted
// version; the scan reports it so the estate does not count a distributed
// primitive as a skill nobody reviewed (TENG-2962).
func TestReadSkillReportsAnInstalledPrimitive(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "recent-mail")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(SkillFileName, "---\nname: recent-mail\ndescription: Lists mail\n---\n")

	skill, ok, err := readSkill(root, "recent-mail")
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if skill.PrimitiveRef != "" {
		t.Fatalf("an ordinary skill must not report a primitive ref, got %q", skill.PrimitiveRef)
	}

	write(PrimitiveMarkerFileName, `{"ref":"dev.telara/recent-mail@0.2.0","artifact_digest":"sha256:ab"}`)
	skill, _, _ = readSkill(root, "recent-mail")
	if skill.PrimitiveRef != "dev.telara/recent-mail@0.2.0" {
		t.Fatalf("PrimitiveRef %q", skill.PrimitiveRef)
	}

	write(PrimitiveMarkerFileName, `not json`)
	skill, _, _ = readSkill(root, "recent-mail")
	if skill.PrimitiveRef != "" {
		t.Fatalf("an unreadable marker must read as an ordinary skill, got %q", skill.PrimitiveRef)
	}
}
