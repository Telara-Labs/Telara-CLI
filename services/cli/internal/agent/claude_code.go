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

// configPath returns the MCP config file path for the given scope.
//
// Global → ~/.claude.json  (Claude Code's user-scope mcpServers)
// Project → ./.mcp.json    (Claude Code project-scope, at repo root)
// Managed → system-wide managed-mcp.json (enterprise lockdown)
func (w *claudeCodeWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".claude.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".mcp.json"), nil
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

// permissionRule returns the Claude Code permission pattern for the given server name.
func permissionRule(serverName string) string {
	return fmt.Sprintf("mcp__%s__*", serverName)
}

// WritePermissions adds an auto-approve rule for all tools from the named MCP server.
func (w *claudeCodeWriter) WritePermissions(scope Scope, serverName string) error {
	path, err := w.settingsPath(scope)
	if err != nil {
		return err
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	if !ensureInStringList(cfg, "permissions", "allow", permissionRule(serverName)) {
		return nil // already present
	}
	return writeJSONConfig(path, cfg)
}

// RemovePermissions removes the auto-approve rule for the named MCP server.
func (w *claudeCodeWriter) RemovePermissions(scope Scope, serverName string) error {
	path, err := w.settingsPath(scope)
	if err != nil {
		return err
	}
	cfg, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	if !removeFromStringList(cfg, "permissions", "allow", permissionRule(serverName)) {
		return nil // not present
	}
	return writeJSONConfig(path, cfg)
}

// settingsPath returns the settings file path for permissions.
// Permissions always go in .claude/settings.json (not .claude.json or .mcp.json),
// since that is where Claude Code reads permission rules from.
func (w *claudeCodeWriter) settingsPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal, ScopeManaged:
		return filepath.Join(w.homeDir, ".claude", "settings.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown scope %d", scope)
	}
}
