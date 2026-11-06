package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

// SnapshotEntry represents a single MCP entry that was removed during logout.
type SnapshotEntry struct {
	Tool       string   `json:"tool"`
	Scope      string   `json:"scope"` // "global", "project", "managed"
	ServerName string   `json:"server_name"`
	Entry      MCPEntry `json:"entry"`
	ProjectDir string   `json:"project_dir,omitempty"` // only for project scope
}

// SaveLogoutSnapshot preserves unresolved entries from an earlier logout.
// Current entries replace the same client location, since they are the most
// recent explicit configuration at that location.
func SaveLogoutSnapshot(entries []SnapshotEntry, userID, tenantID string) error {
	if len(entries) == 0 {
		return nil
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("cannot save client settings without account and workspace identity")
	}
	old, err := LoadWorkspaceSnapshot(userID, tenantID)
	if err != nil {
		return err
	}
	if old == nil {
		return SaveWorkspaceSnapshot(entries, userID, tenantID)
	}
	if old.UserID != userID || old.TenantID != tenantID {
		return fmt.Errorf("existing saved settings belong to another account or workspace")
	}
	type location struct{ tool, scope, server, project string }
	key := func(entry SnapshotEntry) location {
		project := ""
		if entry.Scope == "project" {
			project = filepath.Clean(entry.ProjectDir)
		}
		return location{entry.Tool, entry.Scope, entry.ServerName, project}
	}
	current := make(map[location]bool, len(entries))
	for _, entry := range entries {
		current[key(entry)] = true
	}
	merged := make([]SnapshotEntry, 0, len(old.Entries)+len(entries))
	for _, entry := range old.Entries {
		if !current[key(entry)] {
			merged = append(merged, entry)
		}
	}
	merged = append(merged, entries...)
	return SaveWorkspaceSnapshot(merged, userID, tenantID)
}

// Snapshot holds all MCP entries removed during logout so they can be restored later.
type Snapshot struct {
	CreatedAt string          `json:"created_at"`
	UserID    string          `json:"user_id"`   // immutable user identifier; used to gate restores
	TenantID  string          `json:"tenant_id"` // for managed-layer matching on restore
	Entries   []SnapshotEntry `json:"entries"`
}

func snapshotPath(userID string) (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	return filepath.Join(dir, "mcp-snapshot-"+userID+".enc"), nil
}

// SaveSnapshot encrypts and writes the given entries to the user-specific snapshot file.
func SaveSnapshot(entries []SnapshotEntry, userID, tenantID string) error {
	path, err := snapshotPath(userID)
	if err != nil {
		return err
	}
	return saveSnapshotAt(path, entries, userID, tenantID)
}

func saveSnapshotAt(path string, entries []SnapshotEntry, userID, tenantID string) error {
	if len(entries) == 0 {
		return nil
	}

	snap := Snapshot{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UserID:    userID,
		TenantID:  tenantID,
		Entries:   entries,
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	key, err := getOrCreateSnapshotKey()
	if err != nil {
		return fmt.Errorf("snapshot key: %w", err)
	}

	encrypted, err := encryptAESGCM(key, data)
	if err != nil {
		return fmt.Errorf("encrypt snapshot: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encrypted, 0600); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}

	return nil
}

// LoadSnapshot decrypts and reads the snapshot file for the given user.
// Returns nil, nil if the file does not exist.
func LoadSnapshot(userID string) (*Snapshot, error) {
	path, err := snapshotPath(userID)
	if err != nil {
		return nil, err
	}

	return loadSnapshotAt(path)
}

func loadSnapshotAt(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot: %w", err)
	}

	key, err := getOrCreateSnapshotKey()
	if err != nil {
		return nil, fmt.Errorf("snapshot key: %w", err)
	}

	plaintext, err := decryptAESGCM(key, data)
	if err != nil {
		return nil, fmt.Errorf("decrypt snapshot: %w", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(plaintext, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}

	return &snap, nil
}

// Workspace snapshots isolate the same user operating in different tenants.
// Legacy files are read only for their recorded identity and remain untouched
// until a scoped save has succeeded.
func workspaceSnapshotPath(userID, tenantID string) (string, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(tenantID) == "" {
		return "", fmt.Errorf("account and workspace identity are required")
	}
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s", len(userID), userID, len(tenantID), tenantID)))
	return filepath.Join(dir, fmt.Sprintf("mcp-snapshot-workspace-%x.enc", sum)), nil
}

func LoadWorkspaceSnapshot(userID, tenantID string) (*Snapshot, error) {
	path, err := workspaceSnapshotPath(userID, tenantID)
	if err != nil {
		return nil, err
	}
	snapshot, err := loadSnapshotAt(path)
	if err != nil {
		return nil, err
	}
	if snapshot != nil {
		if snapshot.UserID != userID || snapshot.TenantID != tenantID {
			return nil, fmt.Errorf("saved settings have a mismatched identity")
		}
		return snapshot, nil
	}
	legacy, err := LoadSnapshot(userID)
	if err != nil || legacy == nil {
		return nil, err
	}
	if legacy.UserID != userID {
		return nil, fmt.Errorf("legacy saved settings have a mismatched account")
	}
	if legacy.TenantID == tenantID {
		return legacy, nil
	}
	if legacy.TenantID == "" {
		return nil, fmt.Errorf("legacy saved settings have no workspace identity")
	}
	return nil, nil
}

func SaveWorkspaceSnapshot(entries []SnapshotEntry, userID, tenantID string) error {
	path, err := workspaceSnapshotPath(userID, tenantID)
	if err != nil {
		return err
	}
	if err := saveSnapshotAt(path, entries, userID, tenantID); err != nil {
		return err
	}
	// A matching legacy copy must not reappear after the scoped copy is removed.
	legacy, err := LoadSnapshot(userID)
	if err != nil {
		return err
	}
	if legacy != nil && legacy.UserID == userID && legacy.TenantID == tenantID {
		return DeleteSnapshot(userID)
	}
	return nil
}

func DeleteWorkspaceSnapshot(userID, tenantID string) error {
	path, err := workspaceSnapshotPath(userID, tenantID)
	if err != nil {
		return err
	}
	legacy, err := LoadSnapshot(userID)
	if err != nil {
		return err
	}
	if legacy != nil && legacy.UserID == userID && legacy.TenantID == tenantID {
		if err := DeleteSnapshot(userID); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove workspace snapshot: %w", err)
	}
	return nil
}

// DeleteSnapshot removes the snapshot file for the given user. Not an error if it doesn't exist.
func DeleteSnapshot(userID string) error {
	path, err := snapshotPath(userID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove snapshot: %w", err)
	}
	return nil
}
