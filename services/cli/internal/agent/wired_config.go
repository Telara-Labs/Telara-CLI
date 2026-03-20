package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

// WiredConfig records which MCP configuration was wired at a given scope.
// Stored locally so `telara config` can display names without API calls.
type WiredConfig struct {
	ConfigID   string `json:"config_id"`
	ConfigName string `json:"config_name"`
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
	state, _ := LoadWiredState()
	state.Global = &WiredConfig{ConfigID: configID, ConfigName: configName}
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
