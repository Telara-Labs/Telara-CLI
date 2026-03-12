package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const claudeServersKey = "mcpServers"

// claudeCodeWriter implements AgentWriter for Claude Code.
type claudeCodeWriter struct {
	homeDir string
}

// NewClaudeCodeWriter returns an AgentWriter that configures Claude Code.
func NewClaudeCodeWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &claudeCodeWriter{homeDir: home}
}

func (w *claudeCodeWriter) Name() string { return "claude-code" }

func (w *claudeCodeWriter) Detect() bool {
	return dirExists(filepath.Join(w.homeDir, ".claude")) || binaryInPath("claude")
}

// configPath returns the settings file path for the given scope.
func (w *claudeCodeWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".claude", "settings.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	case ScopeManaged:
		switch runtime.GOOS {
		case "darwin":
			return "/Library/Application Support/ClaudeCode/managed-mcp.json", nil
		case "windows":
			return `C:\ProgramData\ClaudeCode\managed-mcp.json`, nil
		default:
			return "/etc/claude-code/managed-mcp.json", nil
		}
	default:
		return "", fmt.Errorf("unknown scope %d", scope)
	}
}

func (w *claudeCodeWriter) ConfigPath(scope Scope) (string, error) {
	return w.configPath(scope)
}

func (w *claudeCodeWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	if err := writeEntry(path, claudeServersKey, serverName, cfg); err != nil {
		if scope == ScopeManaged && os.IsPermission(err) {
			return fmt.Errorf("writing managed config requires elevated permissions — re-run with sudo (macOS/Linux) or as Administrator (Windows)")
		}
		return err
	}
	return nil
}

func (w *claudeCodeWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readEntries(path, claudeServersKey)
}

func (w *claudeCodeWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeEntry(path, claudeServersKey, serverName)
}
