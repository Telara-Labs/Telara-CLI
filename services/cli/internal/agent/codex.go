package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

const codexServersKey = "mcp_servers"

// codexWriter implements AgentWriter for OpenAI Codex.
type codexWriter struct {
	homeDir string
}

// NewCodexWriter returns an AgentWriter that configures Codex.
func NewCodexWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &codexWriter{homeDir: home}
}

func (w *codexWriter) Name() string { return "codex" }

func (w *codexWriter) Detect() bool {
	return dirExists(filepath.Join(w.homeDir, ".codex")) || binaryInPath("codex")
}

func (w *codexWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".codex", "config.toml"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".codex", "config.toml"), nil
	default:
		return "", fmt.Errorf("codex does not support scope %d", scope)
	}
}

func (w *codexWriter) ConfigPath(scope Scope) (string, error) {
	return w.configPath(scope)
}

func (w *codexWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return writeTOMLEntry(path, codexServersKey, serverName, cfg)
}

func (w *codexWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readTOMLEntries(path, codexServersKey)
}

func (w *codexWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeTOMLEntry(path, codexServersKey, serverName)
}

// WritePermissions sets the enabled_tools field on the server entry with all
// platform tool names.
func (w *codexWriter) WritePermissions(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	tools := PlatformToolNames()
	toolList := make([]interface{}, len(tools))
	for i, t := range tools {
		toolList[i] = t
	}
	return setTOMLServerEntryField(path, codexServersKey, serverName, "enabled_tools", toolList)
}

// RemovePermissions removes the enabled_tools field from the server entry.
func (w *codexWriter) RemovePermissions(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeTOMLServerEntryField(path, codexServersKey, serverName, "enabled_tools")
}
