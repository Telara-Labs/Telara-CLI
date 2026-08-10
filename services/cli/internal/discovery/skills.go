package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skills.go discovers agent skills (SKILL.md) on an enrolled machine.
//
// A skill is executable instruction text that an agent auto-loads into its
// context. It ships with no signature, no review and no provenance, which makes
// it exactly the asset class the AI estate exists to surface — and it was
// previously invisible, because scanner.go only reads MCP configuration.
//
// Skills are NOT another scanSpec row. An MCP config is a key inside one JSON or
// TOML file; a skill is a directory tree of markdown. The two share the
// ConfigScanResult / CollectionScope contract deliberately, so coverage and
// tombstone authority stay defined in exactly one place (see report.go), but the
// traversal itself is separate.
//
// PRIVACY: this scanner reports that a skill EXISTS and what it claims to do —
// never its body. privacy.go already draws that line for configs (paths are
// classed, endpoints are reduced to a host), and a skill body is strictly more
// sensitive: skills routinely carry internal hostnames, ticket IDs and API
// shapes. The content hash gives change detection and cross-machine
// deduplication without moving a single line of the body off the device.

// SkillFileName is the canonical skill entry point. A directory without one is
// not a skill, regardless of what else it contains.
const SkillFileName = "SKILL.md"

// maxSkillFileBytes bounds a single read. A SKILL.md is prose; anything past
// this is not a skill we can meaningfully describe, and an unbounded read on an
// employee machine is a denial-of-service the collector should not offer.
const maxSkillFileBytes = 1 << 20 // 1 MiB

// maxSkillFrontmatterLines bounds frontmatter parsing so a file that opens with
// a delimiter but never closes it cannot be walked to its end.
const maxSkillFrontmatterLines = 200

// DiscoveredSkill describes one skill found on disk.
//
// There is no body field and there must never be one.
type DiscoveredSkill struct {
	SkillName string `json:"skillName"`
	// Description is the skill's own claim about when it applies, taken from
	// frontmatter. It is the single most useful field for a reviewer deciding
	// whether an unreviewed skill is benign, and the skill author already
	// intended it to be read.
	Description string `json:"description,omitempty"`
	// ContentHash is SHA-256 over the raw SKILL.md bytes. Two employees with the
	// same skill produce the same hash, so the logical-skill axis is derivable
	// without the body; a changed hash is a re-review trigger.
	ContentHash string `json:"contentHash"`
	// ReferencedFileCount counts sibling files shipped alongside SKILL.md
	// (scripts, references, assets). A skill that carries executable files is a
	// materially different risk from one that is pure prose.
	ReferencedFileCount int `json:"referencedFileCount"`
	// HasExecutable records whether any sibling file carries an executable bit.
	HasExecutable bool `json:"hasExecutable"`
}

// skillScanSpec is one directory root to search for skills.
type skillScanSpec struct {
	clientFamily string
	scope        string
	path         string
}

// allSkillSpecs returns every skill root this collector knows how to scan.
//
// Only claude-code defines a SKILL.md convention today. Other client families
// are deliberately absent rather than scanned-and-empty: reporting a scope we
// never attempt would inflate coverage with scopes that can never contribute.
func allSkillSpecs() []skillScanSpec {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return []skillScanSpec{
		{ClientClaudeCode, ScopeGlobalSkills, filepath.Join(home, ".claude", "skills")},
		{ClientClaudeCode, ScopeProjectSkills, filepath.Join(cwd, ".claude", "skills")},
	}
}

// ScanSkills scans every known skill root, preserving absence and failure the
// same way ScanAll does.
func ScanSkills() []ConfigScanResult {
	specs := allSkillSpecs()
	results := make([]ConfigScanResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, scanSkillRoot(spec))
	}
	return results
}

// scanSkillRoot reads one skills directory.
//
// An absent root is ScanFileAbsent and therefore COMPLETE — "this machine has no
// global skills" is a real, complete answer that may legitimately retire skills
// seen on a previous scan. An unreadable root is ScanPermissionDenied and
// therefore FAILED, so it can never retire anything.
func scanSkillRoot(spec skillScanSpec) ConfigScanResult {
	result := ConfigScanResult{
		ClientFamily: spec.clientFamily,
		Scope:        spec.scope,
		PathClass:    PathClass(spec.path),
		Servers:      []DiscoveredServer{},
		Skills:       []DiscoveredSkill{},
	}

	if spec.path == "" {
		result.Status = ScanUnsupported
		result.ErrorClass = "unresolved_root"
		return result
	}

	entries, err := os.ReadDir(spec.path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			result.Status = ScanFileAbsent
		case errors.Is(err, fs.ErrPermission):
			result.Status = ScanPermissionDenied
			result.ErrorClass = "permission_denied"
		default:
			result.Status = ScanPermissionDenied
			result.ErrorClass = "read_failed"
		}
		return result
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	// A skill directory that cannot be read degrades this scope to PARTIAL
	// rather than failing it: the other skills in the root were read correctly,
	// and discarding them would lose real evidence. But PARTIAL still withholds
	// tombstone authority, so nothing gets retired on incomplete knowledge.
	partial := false
	for _, name := range names {
		skill, ok, err := readSkill(spec.path, name)
		if err != nil {
			partial = true
			continue
		}
		if !ok {
			continue // a directory without SKILL.md is simply not a skill
		}
		result.Skills = append(result.Skills, skill)
	}

	if partial {
		result.Status = ScanUnsupported
		result.ErrorClass = "skill_read_failed"
		return result
	}
	result.Status = ScanOK
	return result
}

// readSkill reads one skill directory. It returns ok=false when the directory
// holds no SKILL.md, and an error only when a SKILL.md exists but could not be
// read — the caller must distinguish "not a skill" from "a skill we failed to
// see", because only the second is coverage loss.
func readSkill(root, dirName string) (DiscoveredSkill, bool, error) {
	skillPath := filepath.Join(root, dirName, SkillFileName)

	info, err := os.Stat(skillPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DiscoveredSkill{}, false, nil
		}
		return DiscoveredSkill{}, false, err
	}
	if info.IsDir() {
		return DiscoveredSkill{}, false, nil
	}
	if info.Size() > maxSkillFileBytes {
		return DiscoveredSkill{}, false, fmt.Errorf("skill %q exceeds %d bytes", dirName, maxSkillFileBytes)
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		return DiscoveredSkill{}, false, err
	}

	sum := sha256.Sum256(data)
	name, description := parseSkillFrontmatter(string(data))
	if name == "" {
		// Fall back to the directory name. A skill with unparseable or missing
		// frontmatter still EXISTS, and dropping it would hide precisely the
		// malformed, hand-rolled skills most worth looking at.
		name = dirName
	}

	refCount, hasExec := countSkillAssets(filepath.Join(root, dirName))

	return DiscoveredSkill{
		SkillName:           name,
		Description:         description,
		ContentHash:         "sha256:" + hex.EncodeToString(sum[:]),
		ReferencedFileCount: refCount,
		HasExecutable:       hasExec,
	}, true, nil
}

// parseSkillFrontmatter extracts name and description from leading YAML
// frontmatter.
//
// This is a deliberately shallow line reader, not a YAML parser: the collector
// must stay dependency-light (report.go: the CLI does not even depend on
// telara-proto), and only two top-level scalar keys are needed. Nested keys are
// skipped by requiring column-zero indentation, so a `metadata:` block cannot
// contribute a stray `name:`.
func parseSkillFrontmatter(content string) (name, description string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}

	limit := len(lines)
	if limit > maxSkillFrontmatterLines {
		limit = maxSkillFrontmatterLines
	}

	for i := 1; i < limit; i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "---" {
			break
		}
		// Column-zero only: indented lines belong to a nested block.
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description
}

// countSkillAssets counts files shipped alongside SKILL.md and reports whether
// any is executable.
//
// The executable bit is the signal that matters: a skill carrying a runnable
// script is a materially different risk from one that is only prose, and that
// distinction is invisible from the skill's own description.
func countSkillAssets(skillDir string) (count int, hasExecutable bool) {
	_ = filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Best-effort by design: an unreadable asset subtree must not
			// discard the skill we already identified. The skill's own presence
			// is the load-bearing fact; the asset count is descriptive.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Name() == SkillFileName {
			return nil
		}
		count++
		if info, statErr := d.Info(); statErr == nil && info.Mode()&0o111 != 0 {
			hasExecutable = true
		}
		return nil
	})
	return count, hasExecutable
}
