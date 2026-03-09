package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

const windsurfServersKey = "mcpServers"

// windsurfWriter implements AgentWriter for Windsurf.
type windsurfWriter struct {
	homeDir string
}

// NewWindsurfWriter returns an AgentWriter that configures Windsurf.
func NewWindsurfWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &windsurfWriter{homeDir: home}
}

func (w *windsurfWriter) Name() string { return "windsurf" }

func (w *windsurfWriter) Detect() bool {
	return dirExists(filepath.Join(w.homeDir, ".codeium", "windsurf"))
}

func (w *windsurfWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".codeium", "windsurf", "mcp_config.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".windsurf", "mcp_config.json"), nil
	default:
		return "", fmt.Errorf("windsurf does not support scope %d", scope)
	}
}

func (w *windsurfWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return writeEntry(path, windsurfServersKey, serverName, cfg)
}

func (w *windsurfWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readEntries(path, windsurfServersKey)
}

func (w *windsurfWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeEntry(path, windsurfServersKey, serverName)
}
