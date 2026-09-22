package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

// Selection sources for a wired configuration (TENG-3017).
const (
	// SourceExplicit marks a configuration the user named themselves, via
	// `telara config global` / `telara config project` or `telara install
	// --config`. State written before this field existed is treated as
	// explicit, which is what it was: only wireTools persisted state then.
	SourceExplicit = "explicit"
	// SourceDefault marks the user's always-on base configuration, selected
	// by the CLI because the user had made no choice of their own.
	SourceDefault = "default"
)

// WiredConfig records which MCP configuration was wired at a given scope.
// Stored locally so `telara config` can display names without API calls.
type WiredConfig struct {
	ConfigID   string `json:"config_id"`
	ConfigName string `json:"config_name"`
	// Source distinguishes a configuration the user chose from one the CLI
	// defaulted to. It decides whether `telara config` presents the global
	// layer as a choice or as the base it fell back to; it must never be used
	// to decide WHICH config is the base, which is settled by config id from
	// the server (TENG-3017).
	Source string `json:"source,omitempty"`
}

// IsDefault reports whether this configuration was auto-selected rather than
// named by the user. Absent Source means explicit, for backward compatibility
// with state written before the field existed.
func (w *WiredConfig) IsDefault() bool {
	return w != nil && w.Source == SourceDefault
}

// WiredState holds the wired configuration for each scope.
type WiredState struct {
	Global   *WiredConfig            `json:"global,omitempty"`
	Projects map[string]*WiredConfig `json:"projects,omitempty"` // path -> config
}

const wiredStateFile = "wired-state.json"

func wiredStatePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	return filepath.Join(dir, wiredStateFile), nil
}

// LoadWiredState reads the wired state from disk.
// Returns an empty state (not an error) if the file does not exist.
func LoadWiredState() (*WiredState, error) {
	path, err := wiredStatePath()
	if err != nil {
		return &WiredState{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &WiredState{}, nil
		}
		return nil, fmt.Errorf("read wired state: %w", err)
	}

	var state WiredState
	if err := json.Unmarshal(data, &state); err != nil {
		return &WiredState{}, nil // corrupted file — start fresh
	}
	return &state, nil
}

// SaveWiredGlobal records that a config was wired at global scope.
func SaveWiredGlobal(configID, configName string) error {
	return saveWiredGlobal(configID, configName, SourceExplicit)
}

// SaveWiredGlobalDefault records that the CLI wired the user's always-on base
// configuration because the user had chosen nothing. Kept separate from
// SaveWiredGlobal so an auto-selection can never be mistaken for a choice the
// user made, and so a later explicit selection plainly overwrites it.
func SaveWiredGlobalDefault(configID, configName string) error {
	return saveWiredGlobal(configID, configName, SourceDefault)
}

func saveWiredGlobal(configID, configName, source string) error {
	state, _ := LoadWiredState()
	state.Global = &WiredConfig{ConfigID: configID, ConfigName: configName, Source: source}
	return saveWiredState(state)
}

// SaveWiredProject records that a config was wired at project scope for a path.
func SaveWiredProject(projectPath, configID, configName string) error {
	state, _ := LoadWiredState()
	if state.Projects == nil {
		state.Projects = make(map[string]*WiredConfig)
	}
	state.Projects[projectPath] = &WiredConfig{ConfigID: configID, ConfigName: configName}
	return saveWiredState(state)
}

func saveWiredState(state *WiredState) error {
	path, err := wiredStatePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wired state: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write wired state: %w", err)
	}
	return os.Rename(tmp, path)
}
