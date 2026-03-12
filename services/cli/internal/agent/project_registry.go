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

// ProjectRecord tracks a project directory that has been configured with telara init.
type ProjectRecord struct {
	Path      string   `json:"path"`
	Tools     []string `json:"tools"`
	UpdatedAt string   `json:"updated_at"`
}

const projectRegistryFile = "project-paths.json"

func projectRegistryPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	return filepath.Join(dir, projectRegistryFile), nil
}

// RegisterProject adds or updates a project record in the registry.
func RegisterProject(projectDir string, toolName string) error {
	records, err := ListProjects()
	if err != nil {
		return err
	}

	found := false
	for i, r := range records {
		if r.Path == projectDir {
			// Add tool if not already present
			hasTool := false
			for _, t := range r.Tools {
				if t == toolName {
					hasTool = true
					break
				}
			}
			if !hasTool {
				records[i].Tools = append(records[i].Tools, toolName)
			}
			records[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			found = true
			break
		}
	}

	if !found {
		records = append(records, ProjectRecord{
			Path:      projectDir,
			Tools:     []string{toolName},
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	return writeProjectRegistry(records)
}

// ListProjects returns all registered project records.
func ListProjects() ([]ProjectRecord, error) {
	path, err := projectRegistryPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProjectRecord{}, nil
		}
		return nil, fmt.Errorf("read project registry: %w", err)
	}

	var records []ProjectRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse project registry: %w", err)
	}

	return records, nil
}

// UnregisterProject removes a project record from the registry.
func UnregisterProject(projectDir string) error {
	records, err := ListProjects()
	if err != nil {
		return err
	}

	filtered := records[:0]
	for _, r := range records {
		if r.Path != projectDir {
			filtered = append(filtered, r)
		}
	}

	return writeProjectRegistry(filtered)
}

func writeProjectRegistry(records []ProjectRecord) error {
	path, err := projectRegistryPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project registry: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write project registry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename project registry: %w", err)
	}

	return nil
}
