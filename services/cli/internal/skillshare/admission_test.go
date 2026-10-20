package skillshare

import (
	"errors"
	"testing"
)

// The exact bytes an administrator withdrew must not be installable again, and nothing
// else is refused.
func TestAdmitInstall(t *testing.T) {
	body := "---\nname: demo\n---\nwithdrawn bytes\n"
	hash := ContentHash(body)
	other := ContentHash(body + "x")

	t.Run("the exact withdrawn bytes are refused", func(t *testing.T) {
		err := AdmitInstall("demo", hash, body, []DeniedSkill{{ContentHash: hash, Reason: "synthetic removal"}})
		var denied *ErrInstallDenied
		if !errors.As(err, &denied) {
			t.Fatalf("want ErrInstallDenied, got %v", err)
		}
		if denied.ContentHash != hash || denied.Reason != "synthetic removal" {
			t.Fatalf("the refusal must carry the exact hash and the reason: %+v", denied)
		}
	})
	t.Run("no denials admits", func(t *testing.T) {
		if err := AdmitInstall("demo", hash, body, nil); err != nil {
			t.Fatalf("an empty deny list admits: %v", err)
		}
	})
	t.Run("a different hash is not refused, one changed byte is a different skill", func(t *testing.T) {
		if err := AdmitInstall("demo", hash, body, []DeniedSkill{{ContentHash: other}}); err != nil {
			t.Fatalf("a different hash must be admitted: %v", err)
		}
	})
	t.Run("the name is never the key", func(t *testing.T) {
		if err := AdmitInstall("demo", hash, body, []DeniedSkill{{ContentHash: other, SkillName: "demo"}}); err != nil {
			t.Fatalf("a name match alone must not refuse: %v", err)
		}
	})
	t.Run("a denial of the advertised hash refuses even when the received bytes differ", func(t *testing.T) {
		if err := AdmitInstall("demo", other, body, []DeniedSkill{{ContentHash: other}}); err == nil {
			t.Fatal("the advertised hash is denied, so admission must refuse")
		}
	})
	t.Run("blank hashes in the list never match anything", func(t *testing.T) {
		if err := AdmitInstall("demo", "", body, []DeniedSkill{{ContentHash: "  "}, {ContentHash: ""}}); err != nil {
			t.Fatalf("a blank entry must not refuse: %v", err)
		}
	})
	t.Run("surrounding whitespace in an entry still matches", func(t *testing.T) {
		if err := AdmitInstall("demo", hash, body, []DeniedSkill{{ContentHash: " " + hash + "\n"}}); err == nil {
			t.Fatal("a padded hash is the same hash")
		}
	})
}
