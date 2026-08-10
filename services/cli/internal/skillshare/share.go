package skillshare

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/telara-labs/telara-utilities/go/skillscan"
)

// Finding and Severity are re-exported from the shared scanner so this package
// stays the CLI's single surface, while the DETECTION lives in one place both
// the CLI and the enforcing server import. A local copy would drift, and users
// would be blocked by rules their tooling never warned them about.
type Finding = skillscan.Finding

// Severity values, re-exported.
const (
	SeverityCritical = skillscan.SeverityCritical
	SeverityWarn     = skillscan.SeverityWarn
)

// Scan and HasCritical delegate to the shared scanner.
func Scan(body string) []Finding   { return skillscan.Scan(body) }
func HasCritical(f []Finding) bool { return skillscan.HasCritical(f) }

// share.go builds what a `telara skill share` actually sends.
//
// The boundary this file defends: DISCOVERY and SHARING are different acts on
// the same file. `telara scan` reports a skill's existence and hash and never
// opens the body. Sharing uploads the body on purpose. Keeping them in separate
// packages, behind separate commands, with separate consent, is what stops the
// second silently inheriting the first's schedule — the scan runs daily and
// unattended, and a share must never do that.

// Scope is who a shared skill reaches. Ordered by reversibility, not by size.
type Scope string

const (
	// ScopeTeam and ScopeEnterprise are revocable: Telara serves the body, so
	// revoking removes access.
	ScopeTeam       Scope = "team"
	ScopeEnterprise Scope = "enterprise"
	// ScopeOpenSource is NOT revocable in any meaningful sense. Once published
	// it can be crawled, forked and indexed; "revoke" only stops Telara serving
	// it. That asymmetry is why it needs its own confirmation rather than being
	// one more option in a list.
	ScopeOpenSource Scope = "open-source"
)

// ValidScopes is the accepted set, exposed so the command and the server-side
// validator cannot drift into disagreeing about it.
var ValidScopes = []Scope{ScopeTeam, ScopeEnterprise, ScopeOpenSource}

// ParseScope validates a user-supplied scope.
//
// There is deliberately NO default. A skill's audience is the entire decision
// being made here, and defaulting it — to either the narrowest or the widest —
// makes the most consequential field in the command invisible.
func ParseScope(raw string) (Scope, error) {
	s := Scope(strings.ToLower(strings.TrimSpace(raw)))
	for _, v := range ValidScopes {
		if s == v {
			return s, nil
		}
	}
	return "", fmt.Errorf("invalid scope %q (want one of: team, enterprise, open-source)", raw)
}

// IsIrreversible reports whether sharing at this scope cannot be taken back.
// Callers must require an explicit extra confirmation when true.
func (s Scope) IsIrreversible() bool { return s == ScopeOpenSource }

// Skill is one local skill available to share.
type Skill struct {
	Name        string
	Description string
	Version     string
	Path        string
	Body        string
	ContentHash string
	AssetCount  int
}

// ShareRequest is the payload uploaded on share.
//
// Body is present BY DESIGN here and must never appear in a discovery report —
// see the package comment. ScanFindings travels with it so the server records
// what the author was shown and accepted: an acknowledged internal hostname is a
// decision with an owner, and reconstructing that later from logs is not
// possible.
type ShareRequest struct {
	SkillName        string    `json:"skillName"`
	Description      string    `json:"description,omitempty"`
	Version          string    `json:"version,omitempty"`
	Scope            Scope     `json:"scope"`
	ContentHash      string    `json:"contentHash"`
	Body             string    `json:"body"`
	AssetCount       int       `json:"assetCount"`
	ScanFindings     []Finding `json:"scanFindings,omitempty"`
	AcknowledgedRisk bool      `json:"acknowledgedRisk"`
}

// LoadSkill reads one skill from a skills root for sharing.
//
// Unlike the discovery scanner this DOES read the body — that is the whole
// point of the share path — and the caller is required to have obtained consent
// before calling it.
func LoadSkill(root, name string) (Skill, error) {
	dir := filepath.Join(root, name)
	path := filepath.Join(dir, "SKILL.md")

	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %q: %w", name, err)
	}
	sum := sha256.Sum256(raw)

	body := string(raw)
	skillName, description, version := parseFrontmatter(body)
	if skillName == "" {
		skillName = name
	}

	assets := 0
	if entries, derr := os.ReadDir(dir); derr == nil {
		for _, e := range entries {
			if !e.IsDir() && e.Name() != "SKILL.md" {
				assets++
			}
		}
	}

	return Skill{
		Name:        skillName,
		Description: description,
		Version:     version,
		Path:        path,
		Body:        body,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		AssetCount:  assets,
	}, nil
}

// BuildRequest assembles the upload payload and runs the mandatory local scan.
//
// The result is ADVISORY. The server re-scans the same body with the same
// ruleset and its verdict is the one that decides — this scan exists to stop
// secrets leaving the machine at all, and to fail fast before an upload the
// server would refuse anyway.
//
// BuildRequest never decides on the caller's behalf: an automatic redaction that
// guessed wrong would ship a broken skill AND imply the body was reviewed.
func BuildRequest(s Skill, scope Scope, acknowledged bool) (ShareRequest, []Finding) {
	findings := Scan(s.Body)
	return ShareRequest{
		SkillName:        s.Name,
		Description:      s.Description,
		Version:          s.Version,
		Scope:            scope,
		ContentHash:      s.ContentHash,
		Body:             s.Body,
		AssetCount:       s.AssetCount,
		ScanFindings:     findings,
		AcknowledgedRisk: acknowledged,
	}, findings
}

// parseFrontmatter reads name, description and version from leading YAML.
//
// Shallow line reader rather than a YAML dependency, matching the discovery
// scanner: the CLI ships to employee machines and stays dependency-light.
// Column-zero only, so a nested `metadata:` block cannot contribute a stray
// top-level key — except `version`, which conventionally lives under metadata
// and is read at either level.
func parseFrontmatter(content string) (name, description, version string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", ""
	}
	const maxLines = 200
	limit := len(lines)
	if limit > maxLines {
		limit = maxLines
	}
	for i := 1; i < limit; i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "---" {
			break
		}
		indented := line != "" && (line[0] == ' ' || line[0] == '\t')
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			if !indented {
				name = value
			}
		case "description":
			if !indented {
				description = value
			}
		case "version":
			// Accepted at either level: `version:` at top level and
			// `metadata.version:` are both conventional in the wild.
			if version == "" {
				version = value
			}
		}
	}
	return name, description, version
}

// LocalVerdict assesses a body with the shared ruleset, so the CLI can show the
// same score the server will compute.
//
// Shown as a PREVIEW, never as the decision. Presenting it as final would make
// the CLI promise an outcome it cannot guarantee — the user's binary is not the
// enforcement point.
func LocalVerdict(body string) skillscan.Verdict {
	_, v := skillscan.ScanAndAssess(body, skillscan.DefaultPolicy())
	return v
}

// ExplainVerdict renders citations for display.
func ExplainVerdict(v skillscan.Verdict) []string { return skillscan.Explain(v) }
