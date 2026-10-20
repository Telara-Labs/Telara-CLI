package skillshare

// admission.go is the install side of an administrator's removal (TENG-2760).
//
// Enforce takes a withdrawn skill OUT of the load path on the next scan. This is the
// other half. Before `telara skill install` writes bytes into a load path, it asks
// whether those exact bytes were withdrawn, so install cannot put back what a scan
// just took out. The match is the exact content hash, the same key Enforce uses, never
// a name, so an unrelated skill that happens to share a name is not refused and one
// changed byte is a different skill, the tradeoff Enforce already states.

import (
	"fmt"
	"strings"
)

// ErrInstallDenied is returned when the bytes about to be installed were withdrawn by
// an administrator.
type ErrInstallDenied struct {
	Name        string
	ContentHash string
	Reason      string
}

func (e *ErrInstallDenied) Error() string {
	msg := fmt.Sprintf("refusing to install %q: your administrator withdrew these exact bytes (%s)", e.Name, e.ContentHash)
	if r := strings.TrimSpace(e.Reason); r != "" {
		msg += ": " + r
	}
	return msg + ". Ask an administrator to lift the removal if it was a mistake"
}

// AdmitInstall refuses an install whose bytes are on the active deny list.
//
// Both hashes are checked: the one the registry advertised and the one the received
// bytes actually have. WriteSkill already refuses a mismatch, but admission must not
// depend on that order, and a denial of either one is a denial. An empty list admits.
func AdmitInstall(name, advertisedHash, body string, denied []DeniedSkill) error {
	if len(denied) == 0 {
		return nil
	}
	actual := ContentHash(body)
	for _, d := range denied {
		h := strings.TrimSpace(d.ContentHash)
		if h == "" {
			continue
		}
		if h == actual || (strings.TrimSpace(advertisedHash) != "" && h == strings.TrimSpace(advertisedHash)) {
			return &ErrInstallDenied{Name: name, ContentHash: h, Reason: d.Reason}
		}
	}
	return nil
}
