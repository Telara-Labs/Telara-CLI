package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// PrimitiveMarkerFileName marks a skill folder written by `telara tap pull`
// and names the promoted version it holds (TENG-2962).
const PrimitiveMarkerFileName = ".telara-primitive.json"

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
	// PrimitiveRef is set when the skill folder is a TAP primitive installed by
	// `telara tap pull` (publisher/name@version, from PrimitiveMarkerFileName).
	// Such a folder is already distributed through the tenant's registry, so it
	// is not shadow adoption.
	PrimitiveRef string `json:"primitiveRef,omitempty"`
	// SourceLocationKey is an opaque, root-relative package identity used only
	// to keep two independently installed packages with the same frontmatter
	// name from collapsing into one estate assertion. It is never serialized:
	// an employee's directory layout is not estate inventory.
	SourceLocationKey string `json:"-"`
}

// skillScanSpec is one directory root to search for skills.
type skillScanSpec struct {
	clientFamily string
	scope        string
	path         string
}

// allSkillSpecs returns every skill root this collector knows how to scan.
//
// Each entry is a direct `.<client>/skills` root, never a broad recursive
// filesystem crawl. Every client the collector already understands gets both
// its user-global and current-workspace root. That means a newly supported
// client cannot silently inherit MCP discovery while its SKILL.md packages stay
// invisible. Codex's shared .agents roots retain Codex as their consumer but
// get their own scopes, so their tombstone authority never overlaps native
// roots.
func allSkillSpecs() []skillScanSpec {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	clients := []struct {
		family string
		dir    string
	}{
		{ClientClaudeCode, ".claude"},
		{ClientCodex, ".codex"},
		{ClientCursor, ".cursor"},
		{ClientWindsurf, ".windsurf"},
		{ClientVSCode, ".vscode"},
		{ClientGemini, ".gemini"},
		{ClientAmazonQ, ".amazonq"},
	}
	specs := make([]skillScanSpec, 0, len(clients)*2+2)
	for _, client := range clients {
		specs = append(specs,
			skillScanSpec{client.family, ScopeGlobalSkills, filepath.Join(home, client.dir, "skills")},
			skillScanSpec{client.family, ScopeProjectSkills, filepath.Join(cwd, client.dir, "skills")},
		)
	}
	return append(specs,
		skillScanSpec{ClientCodex, ScopeGlobalSharedSkills, filepath.Join(home, ".agents", "skills")},
		skillScanSpec{ClientCodex, ScopeProjectSharedSkills, filepath.Join(cwd, ".agents", "skills")},
	)
}

// SkillRoots returns the directories a skill can be installed into.
//
// Exported so ENFORCEMENT can reach exactly the roots discovery already
// considers, and no others (TENG-2760). Quarantine moves real files, so the set
// of places it may touch has to come from one definition rather than being
// re-derived by the caller — a second copy of these paths is a second thing
// that can drift into pointing somewhere it should not.
func SkillRoots() []string {
	specs := allSkillSpecs()
	roots := make([]string, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.path) != "" {
			roots = append(roots, spec.path)
		}
	}
	return roots
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

	_, err := os.ReadDir(spec.path)
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

	// A skill directory that cannot be read degrades this scope to PARTIAL
	// rather than failing it: the other skills in the root were read correctly,
	// and discarding them would lose real evidence. But PARTIAL still withholds
	// tombstone authority, so nothing gets retired on incomplete knowledge.
	partial := false
	_ = filepath.WalkDir(spec.path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			partial = true
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != SkillFileName {
			return nil
		}
		skill, err := readSkillFile(path)
		if err != nil {
			partial = true
			return nil
		}
		skill.SourceLocationKey = opaqueSkillLocationKey(spec.path, filepath.Dir(path))
		result.Skills = append(result.Skills, skill)
		return nil
	})
	sort.Slice(result.Skills, func(i, j int) bool { return result.Skills[i].SkillName < result.Skills[j].SkillName })

	if partial {
		result.Status = ScanUnsupported
		result.ErrorClass = "skill_read_failed"
		return result
	}
	result.Status = ScanOK
	return result
}

func opaqueSkillLocationKey(root, skillDir string) string {
	relative, err := filepath.Rel(root, skillDir)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		// This is local discovery, so an impossible relative path must not become
		// a raw-path fallback in the report. The caller still reports the skill
		// by content/name, but an empty key makes the anomaly visible to tests.
		return ""
	}
	sum := sha256.Sum256([]byte("telara-skill-location/v1\x00" + filepath.ToSlash(relative)))
	return hex.EncodeToString(sum[:12])
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
	skill, err := readSkillFile(skillPath)
	return skill, true, err
}

func readSkillFile(skillPath string) (DiscoveredSkill, error) {
	info, err := os.Stat(skillPath)
	if err != nil {
		return DiscoveredSkill{}, err
	}
	if info.Size() > maxSkillFileBytes {
		return DiscoveredSkill{}, fmt.Errorf("skill %q exceeds %d bytes", filepath.Base(filepath.Dir(skillPath)), maxSkillFileBytes)
	}

	data, err := os.ReadFile(skillPath)
	if err != nil {
		return DiscoveredSkill{}, err
	}

	sum := sha256.Sum256(data)
	name, description := parseSkillFrontmatter(string(data))
	if name == "" {
		// Fall back to the directory name. A skill with unparseable or missing
		// frontmatter still EXISTS, and dropping it would hide precisely the
		// malformed, hand-rolled skills most worth looking at.
		name = filepath.Base(filepath.Dir(skillPath))
	}

	refCount, hasExec := countSkillAssets(filepath.Dir(skillPath))

	return DiscoveredSkill{
		SkillName:           name,
		Description:         description,
		ContentHash:         "sha256:" + hex.EncodeToString(sum[:]),
		ReferencedFileCount: refCount,
		HasExecutable:       hasExec,
		PrimitiveRef:        primitiveRef(filepath.Dir(skillPath)),
	}, nil
}

// primitiveRef reads the ref from an installed primitive's marker, or "" when
// the folder is not one. A marker that cannot be read is treated as absent:
// the skill is then reported as an ordinary skill, the conservative reading.
func primitiveRef(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, PrimitiveMarkerFileName))
	if err != nil || len(raw) > 4096 {
		return ""
	}
	var m struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.Ref)
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
