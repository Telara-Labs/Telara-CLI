package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// VS Code uses "servers" (not "mcpServers") as the top-level key in mcp.json.
const vscodeServersKey = "servers"

// vscodeWriter implements AgentWriter for VS Code.
// VS Code reads MCP servers from the user profile or a project; there is no managed layer.
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
	case ScopeGlobal:
		// The user profile's mcp.json. It must never be the working directory,
		// where the bearer key would sit in a repository's tree.
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine the VS Code user configuration directory: %w", err)
		}
		return filepath.Join(dir, "Code", "User", "mcp.json"), nil
	default:
		return "", fmt.Errorf("vscode does not support scope %d", scope)
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

