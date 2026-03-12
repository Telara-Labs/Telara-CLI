package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
)

// SnapshotEntry represents a single MCP entry that was removed during logout.
type SnapshotEntry struct {
	Tool       string   `json:"tool"`
	Scope      string   `json:"scope"`                 // "global", "project", "managed"
	ServerName string   `json:"server_name"`
	Entry      MCPEntry `json:"entry"`
	ProjectDir string   `json:"project_dir,omitempty"` // only for project scope
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

	path, err := snapshotPath(userID)
	if err != nil {
		return err
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
