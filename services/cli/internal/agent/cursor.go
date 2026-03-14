package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

const cursorServersKey = "mcpServers"

// cursorWriter implements AgentWriter for Cursor.
type cursorWriter struct {
	homeDir string
}

// NewCursorWriter returns an AgentWriter that configures Cursor.
func NewCursorWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &cursorWriter{homeDir: home}
}

func (w *cursorWriter) Name() string { return "cursor" }

func (w *cursorWriter) Detect() bool {
	return dirExists(filepath.Join(w.homeDir, ".cursor"))
}

func (w *cursorWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".cursor", "mcp.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".cursor", "mcp.json"), nil
	default:
		return "", fmt.Errorf("cursor does not support scope %d", scope)
	}
}

func (w *cursorWriter) ConfigPath(scope Scope) (string, error) {
	return w.configPath(scope)
}

func (w *cursorWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return writeEntry(path, cursorServersKey, serverName, cfg)
}

func (w *cursorWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readEntries(path, cursorServersKey)
}

func (w *cursorWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeEntry(path, cursorServersKey, serverName)
}

// WritePermissions sets the autoApprove field on the server entry with all
// platform tool names.
func (w *cursorWriter) WritePermissions(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	tools := PlatformToolNames()
	toolList := make([]interface{}, len(tools))
	for i, t := range tools {
		toolList[i] = t
	}
	return setServerEntryField(path, cursorServersKey, serverName, "autoApprove", toolList)
}

// RemovePermissions removes the autoApprove field from the server entry.
func (w *cursorWriter) RemovePermissions(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeServerEntryField(path, cursorServersKey, serverName, "autoApprove")
}
