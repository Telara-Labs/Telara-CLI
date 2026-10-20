package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

// TENG-2760. `telara scan` quarantines the exact bytes an administrator withdrew, and
// `telara skill install` used to put them straight back into the load path. These drive
// the real install code, the real WriteSkill and the real Enforce, with a fake registry
// that returns a chosen withdrawn list.

type fakeDenied struct {
	list []skillshare.DeniedSkill
	err  error
}

func (f fakeDenied) ListDeniedSkills(context.Context) ([]skillshare.DeniedSkill, error) {
	return f.list, f.err
}

const admissionBody = "---\nname: pgv2-admission\ndescription: synthetic withdrawn skill\n---\n\nSynthetic harmless marker.\n"

func admissionDetail(body string) *api.SharedSkillDetail {
	d := &api.SharedSkillDetail{Body: body}
	d.Name = "pgv2-admission"
	d.ContentHash = skillshare.ContentHash(body)
	return d
}

func skillFile(dir string) string { return filepath.Join(dir, "pgv2-admission", "SKILL.md") }

func TestInstallRefusesTheExactBytesAnAdministratorWithdrew(t *testing.T) {
	dir := t.TempDir()
	detail := admissionDetail(admissionBody)
	denied := fakeDenied{list: []skillshare.DeniedSkill{{ContentHash: detail.ContentHash, Reason: "synthetic removal"}}}

	for _, force := range []bool{false, true} {
		err := installSharedSkill(context.Background(), denied, detail, []installTarget{{client: "claude-code", dir: dir}}, force, false)
		var refused *skillshare.ErrInstallDenied
		if !errors.As(err, &refused) {
			t.Fatalf("force=%t: want a refusal for withdrawn bytes, got %v", force, err)
		}
		if _, statErr := os.Stat(skillFile(dir)); !os.IsNotExist(statErr) {
			t.Fatalf("force=%t: nothing may be written for withdrawn bytes", force)
		}
	}
}

func TestInstallDryRunDoesNotPromiseAWithdrawnInstall(t *testing.T) {
	dir := t.TempDir()
	detail := admissionDetail(admissionBody)
	denied := fakeDenied{list: []skillshare.DeniedSkill{{ContentHash: detail.ContentHash}}}
	if err := installSharedSkill(context.Background(), denied, detail, []installTarget{{client: "claude-code", dir: dir}}, false, true); err == nil {
		t.Fatal("a dry run of withdrawn bytes must be refused too")
	}
}

func TestInstallFailsClosedWhenTheWithdrawnListCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	detail := admissionDetail(admissionBody)
	err := installSharedSkill(context.Background(), fakeDenied{err: errors.New("connection refused")}, detail, []installTarget{{client: "claude-code", dir: dir}}, true, false)
	if err == nil || !strings.Contains(err.Error(), "nothing was installed") {
		t.Fatalf("an unreadable withdrawn list must stop the install and say so: %v", err)
	}
	if _, statErr := os.Stat(skillFile(dir)); !os.IsNotExist(statErr) {
		t.Fatal("nothing may be written when the withdrawn list cannot be confirmed")
	}
}

func TestInstallStillInstallsWhatWasNeverWithdrawnAndWhatWasLifted(t *testing.T) {
	detail := admissionDetail(admissionBody)
	for name, list := range map[string][]skillshare.DeniedSkill{
		"never withdrawn":                        nil,
		"another skill's bytes were withdrawn":   {{ContentHash: skillshare.ContentHash("something else")}},
		"the withdrawal was lifted (not listed)": {},
		"the same name but different bytes":      {{ContentHash: skillshare.ContentHash(admissionBody + "x"), SkillName: "pgv2-admission"}},
	} {
		dir := t.TempDir()
		if err := installSharedSkill(context.Background(), fakeDenied{list: list}, detail, []installTarget{{client: "claude-code", dir: dir}}, false, false); err != nil {
			t.Fatalf("%s: install must work: %v", name, err)
		}
		got, err := os.ReadFile(skillFile(dir))
		if err != nil || string(got) != admissionBody {
			t.Fatalf("%s: the installed bytes must be exact: %v", name, err)
		}
	}
}

// The whole C29-01 sequence in one process: install, an administrator withdraws the bytes,
// scan quarantines them without deleting, install is refused, the quarantined copy is
// intact, and once the withdrawal is lifted install works again.
func TestWithdrawnSkillStaysOutOfTheLoadPathAndQuarantineIsPreserved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Skipf("this platform does not take HOME from the environment: %q %v", got, err)
	}
	root := filepath.Join(home, ".claude", "skills")
	detail := admissionDetail(admissionBody)
	targets := []installTarget{{client: "claude-code", dir: root}}

	if err := installSharedSkill(context.Background(), fakeDenied{}, detail, targets, false, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	withdrawn := []skillshare.DeniedSkill{{ContentHash: detail.ContentHash, SkillName: detail.Name, Reason: "synthetic C29 quarantine fixture"}}
	actions, err := skillshare.Enforce(home, withdrawn)
	if err != nil || len(actions) != 1 {
		t.Fatalf("enforcement must quarantine the one installed copy: %v %v", actions, err)
	}
	if _, statErr := os.Stat(skillFile(root)); !os.IsNotExist(statErr) {
		t.Fatal("the withdrawn skill must have left the load path")
	}
	quarantined := filepath.Join(actions[0].To, "SKILL.md")
	if kept, readErr := os.ReadFile(quarantined); readErr != nil || string(kept) != admissionBody {
		t.Fatalf("quarantine must keep byte-identical content: %v", readErr)
	}

	// The reinstall that used to succeed.
	if err := installSharedSkill(context.Background(), fakeDenied{list: withdrawn}, detail, targets, false, false); err == nil {
		t.Fatal("install must refuse the bytes the scan just quarantined")
	}
	if _, statErr := os.Stat(skillFile(root)); !os.IsNotExist(statErr) {
		t.Fatal("the withdrawn skill must not be back in the load path")
	}
	if kept, readErr := os.ReadFile(quarantined); readErr != nil || string(kept) != admissionBody {
		t.Fatal("a refused install must leave the quarantined copy untouched")
	}

	// Reversible: once the administrator lifts the removal, install works again.
	if err := installSharedSkill(context.Background(), fakeDenied{}, detail, targets, false, false); err != nil {
		t.Fatalf("after the removal is lifted, install must work: %v", err)
	}
}
