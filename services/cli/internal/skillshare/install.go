package skillshare

// install.go is the write side of skill sharing (TENG-2720).
//
// share.go reads a local skill and uploads it; this takes an approved registry
// entry and puts it on disk. The two are deliberately symmetric about the
// content hash: share computes it, install VERIFIES it, and the equality
// between them is what lets the AI estate join an installed skill to the entry
// somebody reviewed and approved.
//
// A skill is instruction text the agent loads and obeys. Writing one to disk is
// therefore not a file copy — it is granting something authority inside every
// future session in this workspace. Everything below is shaped by that.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// InstallResult describes what WriteSkill did, for the command layer to print.
type InstallResult struct {
	Path        string
	Overwrote   bool
	ContentHash string
}

// ErrLocalModification is returned when the on-disk skill differs from every
// version the registry knows about, so overwriting would destroy local edits.
type ErrLocalModification struct {
	Path string
}

func (e *ErrLocalModification) Error() string {
	return fmt.Sprintf("%s has local modifications that are not in the registry", e.Path)
}

// WriteSkill installs one skill into root/<name>/SKILL.md.
//
// Refuses, in this order, before anything touches the filesystem:
//  1. a body whose sha256 disagrees with the hash the registry advertised
//  2. a body that trips a CRITICAL scan rule
//  3. an existing local file that differs from what we are about to write,
//     unless force
func WriteSkill(root, name, body, contentHash string, force bool) (*InstallResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	// A name is about to become a directory. Anything path-shaped in it would
	// let a registry entry decide where on the filesystem it lands.
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("refusing to install skill with unsafe name %q", name)
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("skill %q has an empty body", name)
	}

	// 1. HASH VERIFICATION, before the bytes go anywhere.
	//
	// content_hash is what the approver approved and what `telara scan` reports
	// for an installed file. Writing bytes that hash differently would break
	// that join silently: the estate would show an installed skill matching no
	// approved entry, and nobody could tell whether it had been tampered with
	// in transit or merely mis-recorded.
	actual := ContentHash(body)
	if contentHash != "" && actual != contentHash {
		return nil, fmt.Errorf(
			"content hash mismatch for %q: registry advertised %s, received bytes hash to %s — "+
				"refusing to install", name, contentHash, actual)
	}

	// 2. LOCAL RE-SCAN.
	//
	// The server already refused to store anything that trips the policy, so
	// this should never fire. It runs anyway because it is the last check that
	// happens on the user's own machine, with no trust in the network path or
	// in the endpoint being the one we think it is.
	verdict := LocalVerdict(body)
	var critical []string
	for _, f := range verdict.FiredRules {
		if f.Severity == SeverityCritical {
			critical = append(critical, fmt.Sprintf("%s %s (%d occurrence(s))", f.RuleID, f.Name, f.Occurrences))
		}
	}
	if len(critical) > 0 {
		return nil, fmt.Errorf(
			"refusing to install %q: the received body trips %s. "+
				"The server should never have served this — report it before using this skill",
			name, strings.Join(critical, ", "))
	}

	dir := filepath.Join(root, name)
	path := filepath.Join(dir, discovery.SkillFileName)

	// 3. NO SILENT CLOBBER.
	existing, err := os.ReadFile(path)
	overwrote := false
	switch {
	case err == nil:
		overwrote = true
		if string(existing) == body {
			// Already exactly this. Nothing to do, and reporting it as an
			// install would misdescribe what happened.
			return &InstallResult{Path: path, Overwrote: false, ContentHash: actual}, nil
		}
		if !force {
			return nil, &ErrLocalModification{Path: path}
		}
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("read existing skill at %s: %w", path, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create skill directory %s: %w", dir, err)
	}

	// Atomic: a crash mid-write must not leave a half-written SKILL.md that an
	// agent would still load and follow.
	tmp, err := os.CreateTemp(dir, "."+discovery.SkillFileName+".*")
	if err != nil {
		return nil, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write skill body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return nil, fmt.Errorf("set skill file mode: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, fmt.Errorf("install skill to %s: %w", path, err)
	}

	return &InstallResult{Path: path, Overwrote: overwrote, ContentHash: actual}, nil
}

// ContentHash is the registry's hash encoding, shared by share and install.
//
// One function so the two sides cannot drift: a different encoding on either
// side would break the equality the estate join depends on, and would do it
// silently.
func ContentHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}
