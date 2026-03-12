package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// VS Code uses "servers" (not "mcpServers") as the top-level key in mcp.json.
const vscodeServersKey = "servers"

// vscodeWriter implements AgentWriter for VS Code.
// VS Code only supports project-scope MCP configuration.
type vscodeWriter struct {
	homeDir string
}

// NewVSCodeWriter returns an AgentWriter that configures VS Code.
func NewVSCodeWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &vscodeWriter{homeDir: home}
}

func (w *vscodeWriter) Name() string { return "vscode" }

func (w *vscodeWriter) Detect() bool {
	return binaryInPath("code") || dirExists(filepath.Join(w.homeDir, ".vscode"))
}

func (w *vscodeWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".vscode", "mcp.json"), nil
	default:
		// VS Code only supports project-scope MCP config; fall back to project.
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".vscode", "mcp.json"), nil
	}
}

func (w *vscodeWriter) ConfigPath(scope Scope) (string, error) {
	return w.configPath(scope)
}

func (w *vscodeWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return writeEntry(path, vscodeServersKey, serverName, cfg)
}

func (w *vscodeWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readEntries(path, vscodeServersKey)
}

func (w *vscodeWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeEntry(path, vscodeServersKey, serverName)
}

