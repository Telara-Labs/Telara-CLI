package skillshare

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// The restore path (TENG-3921, extends TENG-2760). Block
// telara-documentation/architecture/estate-remediation.md.
//
// A withdrawn skill is moved to ~/.telara/quarantine/skills with an origin
// record. When an administrator lifts the removal, its content hash leaves the
// deny list, and the next scan moves the skill back to where it was. Nothing
// is merged: a skill that has since reappeared at that path is left alone and
// the quarantined copy is kept. A quarantine made before origin records
// existed has no record and stays where it is; its REMOVED.md says how to ask
// for it back.

// OriginFileName is the record a reinstate reads.
const OriginFileName = "ORIGIN.json"

type originRecord struct {
	SkillName   string `json:"skill_name"`
	ContentHash string `json:"content_hash"`
	From        string `json:"from"`
	Root        string `json:"root"`
	Retained    string `json:"retained,omitempty"`
}

// ReinstateAction reports one quarantined skill put back, or left and why.
type ReinstateAction struct {
	SkillName   string `json:"skillName"`
	ContentHash string `json:"contentHash"`
	From        string `json:"from"`
	To          string `json:"to"`
	Kind        string `json:"kind"` // reinstated | conflict
	Note        string `json:"note,omitempty"`
}

func writeOrigin(dir string, rec originRecord) error {
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, OriginFileName), body, 0o600)
}

// Reinstate moves back every quarantined skill whose content hash is no longer
// denied. Call it only with a deny list that was actually fetched: an empty
// list from a failed fetch would put every withdrawn skill back.
func Reinstate(homeDir string, denied []DeniedSkill) ([]ReinstateAction, error) {
	still := make(map[string]bool, len(denied))
	for _, d := range denied {
		if h := strings.TrimSpace(d.ContentHash); h != "" {
			still[h] = true
		}
	}
	entries, err := os.ReadDir(QuarantineRoot(homeDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	roots := map[string]bool{}
	for _, r := range discovery.SkillRoots() {
		roots[filepath.Clean(r)] = true
	}
	var actions []ReinstateAction
	var failures []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(QuarantineRoot(homeDir), e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, OriginFileName))
		if errors.Is(err, os.ErrNotExist) {
			continue // quarantined before origin records existed
		}
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		var rec originRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		if rec.ContentHash == "" || still[rec.ContentHash] {
			continue
		}
		act, err := reinstateOne(dir, rec, roots)
		if act != nil {
			actions = append(actions, *act)
		}
		if err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return actions, fmt.Errorf("reinstate incomplete: %s", strings.Join(failures, "; "))
	}
	return actions, nil
}

func reinstateOne(dir string, rec originRecord, roots map[string]bool) (*ReinstateAction, error) {
	act := &ReinstateAction{SkillName: rec.SkillName, ContentHash: rec.ContentHash, From: dir, To: rec.From}
	// The destination must be where discovery looks for skills, and inside the
	// root it came from. The record is ours, but this moves files.
	root := filepath.Clean(rec.Root)
	rel, err := filepath.Rel(root, rec.From)
	if !roots[root] || err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("refusing to reinstate %s to %s: not inside a skill root", rec.SkillName, rec.From)
	}
	if _, err := os.Lstat(rec.From); err == nil {
		act.Kind, act.Note = "conflict", "a skill exists at the original path again; the quarantined copy is kept"
		return act, nil
	}
	// Check the content is still the approved bytes before it goes back.
	body, err := os.ReadFile(filepath.Join(dir, discovery.SkillFileName))
	if err != nil {
		return nil, fmt.Errorf("read quarantined %s: %w", rec.SkillName, err)
	}
	if ContentHash(string(body)) != rec.ContentHash {
		act.Kind, act.Note = "conflict", "the quarantined copy no longer matches the hash that was lifted; it is kept"
		return act, nil
	}
	if err := os.MkdirAll(filepath.Dir(rec.From), 0o755); err != nil {
		return nil, err
	}
	if err := renameDir(dir, rec.From); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			act.Kind, act.Note = "conflict", "the quarantine is on a different disk from the skill root; copy "+dir+" back by hand"
			return act, nil
		}
		return nil, fmt.Errorf("move %s back: %w", rec.SkillName, err)
	}
	for _, f := range []string{"REMOVED.md", OriginFileName} {
		if err := os.Remove(filepath.Join(rec.From, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reinstated %s but could not remove %s: %w", rec.SkillName, f, err)
		}
	}
	act.Kind = "reinstated"
	return act, nil
}
