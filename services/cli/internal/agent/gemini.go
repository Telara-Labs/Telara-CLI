package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

const geminiServersKey = "mcpServers"

// geminiWriter implements AgentWriter for Google Gemini CLI.
type geminiWriter struct {
	homeDir string
}

// NewGeminiWriter returns an AgentWriter that configures Gemini CLI.
func NewGeminiWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &geminiWriter{homeDir: home}
}

func (w *geminiWriter) Name() string { return "gemini" }

func (w *geminiWriter) Detect() bool {
	return dirExists(filepath.Join(w.homeDir, ".gemini")) || binaryInPath("gemini")
}

func (w *geminiWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".gemini", "settings.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".gemini", "settings.json"), nil
	default:
		return "", fmt.Errorf("gemini does not support scope %d", scope)
	}
}

func (w *geminiWriter) ConfigPath(scope Scope) (string, error) {
	return w.configPath(scope)
}

func (w *geminiWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return writeEntry(path, geminiServersKey, serverName, cfg)
}

func (w *geminiWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readEntries(path, geminiServersKey)
}

func (w *geminiWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeEntry(path, geminiServersKey, serverName)
}

// WritePermissions sets trust=true on the server entry for auto-approval.
func (w *geminiWriter) WritePermissions(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return setServerEntryField(path, geminiServersKey, serverName, "trust", true)
}

// RemovePermissions removes the trust field from the server entry.
func (w *geminiWriter) RemovePermissions(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeServerEntryField(path, geminiServersKey, serverName, "trust")
}
