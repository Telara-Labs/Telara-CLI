package skillshare

// quarantine.go enforces an admin's removal on this machine (TENG-2760).
//
// Revoking a skill stopped Telara SERVING it and did nothing about the copy
// already on disk, which keeps loading forever. For instruction text an agent
// reads and obeys, that made removal advisory: the thing an admin removed was
// still running everywhere it had been installed.
//
// QUARANTINE, NOT DELETE. The skill is moved out of the load path rather than
// destroyed. That is not squeamishness — this control also covers skills a user
// WROTE THEMSELVES and never shared, because an admin can remove anything
// deployed in the tenant, and deleting those destroys an employee's own work.
// Moving stops it loading, which is the entire security goal, while remaining
// reversible if the removal was a mistake and leaving the person evidence of
// what happened and how to ask for it back.
//
// This is the one place the CLI writes outside its own config. `telara scan`
// otherwise reports that a skill EXISTS and never its body; enforcement makes
// it an agent that moves files on somebody's machine, which the command says
// out loud rather than doing quietly.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// DeniedSkill is one active removal, as the server reports it.
type DeniedSkill struct {
	ContentHash string `json:"content_hash"`
	SkillName   string `json:"skill_name,omitempty"`
	Reason      string `json:"reason,omitempty"`
	DeniedBy    string `json:"denied_by,omitempty"`
	DeniedAt    string `json:"denied_at,omitempty"`
}

// QuarantineAction records one enforcement, for the operator and for the report.
type QuarantineAction struct {
	SkillName   string `json:"skillName"`
	ContentHash string `json:"contentHash"`
	From        string `json:"from"`
	To          string `json:"to"`
	Reason      string `json:"reason,omitempty"`
}

// QuarantineRoot is where withdrawn skills go.
//
// Under ~/.telara rather than beside the skills directory on purpose: anything
// left inside a client's skills root risks being re-discovered as a skill, and
// the point is to take it OUT of every load path, not to rename it in place.
func QuarantineRoot(homeDir string) string {
	return filepath.Join(homeDir, ".telara", "quarantine", "skills")
}

// Enforce quarantines every installed skill whose content hash appears in the
// tenant's deny list.
//
// Matching is by CONTENT HASH ONLY. An admin refused specific bytes, and a name
// match would quarantine an unrelated skill somebody happens to have named the
// same thing — expensive, because this moves real files. The tradeoff is stated
// rather than hidden: editing one byte produces a different hash and escapes
// the entry. That copy is not invisible, though — its hash then matches no
// approved entry, which is exactly the drift the estate already surfaces.
func Enforce(homeDir string, denied []DeniedSkill) ([]QuarantineAction, error) {
	if len(denied) == 0 {
		return nil, nil
	}
	byHash := make(map[string]DeniedSkill, len(denied))
	for _, d := range denied {
		if h := strings.TrimSpace(d.ContentHash); h != "" {
			byHash[h] = d
		}
	}
	if len(byHash) == 0 {
		return nil, nil
	}

	var actions []QuarantineAction
	var failures []string

	// The roots come from discovery's own definition, so enforcement can only
	// ever reach a directory discovery already considers a skill root. A deny
	// entry names a HASH, never a path, so nothing the server sends can steer
	// this somewhere else.
	for _, root := range discovery.SkillRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			// An absent root is the ordinary case on most machines.
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			hash, ok := installedSkillHash(root, e.Name())
			if !ok {
				continue
			}
			denial, denied := byHash[hash]
			if !denied {
				continue
			}
			action, qerr := quarantineOne(homeDir, root, e.Name(), denial)
			if qerr != nil {
				// One skill failing must not stop the others: a machine with two
				// withdrawn skills should not keep the second because the first
				// hit a permissions problem.
				failures = append(failures, qerr.Error())
				continue
			}
			if action != nil {
				actions = append(actions, *action)
			}
		}
	}
	if len(failures) > 0 {
		return actions, fmt.Errorf("quarantine incomplete: %s", strings.Join(failures, "; "))
	}
	return actions, nil
}

// installedSkillHash reads one installed skill's content hash.
//
// Computed here rather than taken from the discovery report so enforcement acts
// on what is on disk RIGHT NOW. A report built moments earlier is a description
// of the past, and this moves files.
func installedSkillHash(root, name string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, name, discovery.SkillFileName))
	if err != nil {
		return "", false
	}
	return ContentHash(string(raw)), true
}

// quarantineOne moves a single skill directory out of the load path.
func quarantineOne(homeDir, root, name string, entry DeniedSkill) (*QuarantineAction, error) {
	// Refuse anything path-shaped before touching the filesystem. The name comes
	// from a directory we enumerated rather than from the server, but this is
	// the operation that moves files, so it re-checks rather than trusting a
	// caller — the same reasoning as the install path.
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("refusing to quarantine unsafe skill name %q", name)
	}
	from := filepath.Join(root, name)

	// Confirm the directory really sits under the root we enumerated. A symlink
	// pointing elsewhere must not turn enforcement into a way to move arbitrary
	// directories.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve skills root: %w", err)
	}
	realFrom, err := filepath.EvalSymlinks(from)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve skill directory: %w", err)
	}
	if !strings.HasPrefix(realFrom, realRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("refusing to quarantine %q: it resolves outside %s", name, realRoot)
	}

	// Timestamped so repeated removals of a re-created skill each keep their
	// evidence instead of overwriting the previous one.
	//
	// The collision suffix is load-bearing, not defensive padding: a user who
	// re-creates a withdrawn skill and scans again within the same second would
	// otherwise produce the same destination, and renaming onto an existing
	// non-empty directory FAILS — so the enforcement would silently not happen
	// and the skill would stay in the load path. Which is precisely the case
	// this control exists for.
	base := filepath.Join(QuarantineRoot(homeDir),
		fmt.Sprintf("%s-%s", name, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return nil, fmt.Errorf("create quarantine directory: %w", err)
	}
	to := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(to); os.IsNotExist(err) {
			break
		}
		if i > 100 {
			return nil, fmt.Errorf("quarantine destination %s is occupied", base)
		}
		to = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.Rename(from, to); err != nil {
		return nil, fmt.Errorf("move skill to quarantine: %w", err)
	}

	if err := os.WriteFile(filepath.Join(to, "REMOVED.md"),
		[]byte(removalNote(name, entry)), 0o644); err != nil {
		// The move already happened and is the part that matters. A missing
		// note is worse than useless silence, so report it rather than
		// pretending the enforcement was clean.
		return nil, fmt.Errorf("write removal note: %w", err)
	}

	return &QuarantineAction{
		SkillName: name, ContentHash: entry.ContentHash,
		From: from, To: to, Reason: entry.Reason,
	}, nil
}

// removalNote is what the person whose skill was moved actually reads.
//
// It has to answer three questions or it reads as the tool malfunctioning:
// what happened, who decided, and what to do about it. A removal with no
// explanation invites re-creating the file, which is how enforcement turns into
// a loop nobody understands.
func removalNote(name string, entry DeniedSkill) string {
	var b strings.Builder
	b.WriteString("# " + name + " was removed by your administrator\n\n")
	b.WriteString("This skill is no longer active. It was moved here so your agent stops\n")
	b.WriteString("loading it. Nothing was deleted — the original content is in this folder.\n\n")
	if entry.Reason != "" {
		b.WriteString("## Reason\n\n" + entry.Reason + "\n\n")
	}
	if entry.DeniedBy != "" {
		b.WriteString("Removed by: " + entry.DeniedBy + "\n")
	}
	if entry.DeniedAt != "" {
		b.WriteString("Removed at: " + entry.DeniedAt + "\n")
	}
	if entry.ContentHash != "" {
		b.WriteString("Content hash: " + entry.ContentHash + "\n")
	}
	b.WriteString("\n## If you still need it\n\n")
	b.WriteString("Ask an administrator to reinstate it. Removals are kept as records\n")
	b.WriteString("precisely so a skill can be requested back.\n\n")
	b.WriteString("Re-creating the file will not restore it: the next scan will move it\n")
	b.WriteString("here again, and repeated re-creation is visible to your administrator.\n")
	return b.String()
}
