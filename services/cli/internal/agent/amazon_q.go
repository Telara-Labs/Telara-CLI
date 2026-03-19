package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

const amazonQServersKey = "mcpServers"

// amazonQWriter implements AgentWriter for Amazon Q Developer.
type amazonQWriter struct {
	homeDir string
}

// NewAmazonQWriter returns an AgentWriter that configures Amazon Q Developer.
func NewAmazonQWriter() AgentWriter {
	home, _ := os.UserHomeDir()
	return &amazonQWriter{homeDir: home}
}

func (w *amazonQWriter) Name() string { return "amazon-q" }

func (w *amazonQWriter) Detect() bool {
	return dirExists(filepath.Join(w.homeDir, ".aws", "amazonq")) ||
		binaryInPath("q") || binaryInPath("qchat")
}

func (w *amazonQWriter) configPath(scope Scope) (string, error) {
	switch scope {
	case ScopeGlobal:
		return filepath.Join(w.homeDir, ".aws", "amazonq", "mcp.json"), nil
	case ScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
		return filepath.Join(cwd, ".amazonq", "mcp.json"), nil
	default:
		return "", fmt.Errorf("amazon-q does not support scope %d", scope)
	}
}

func (w *amazonQWriter) ConfigPath(scope Scope) (string, error) {
	return w.configPath(scope)
}

func (w *amazonQWriter) Write(scope Scope, serverName string, cfg MCPEntry) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return writeEntry(path, amazonQServersKey, serverName, cfg)
}

func (w *amazonQWriter) Read(scope Scope) (map[string]MCPEntry, error) {
	path, err := w.configPath(scope)
	if err != nil {
		return nil, err
	}
	return readEntries(path, amazonQServersKey)
}

func (w *amazonQWriter) Remove(scope Scope, serverName string) error {
	path, err := w.configPath(scope)
	if err != nil {
		return err
	}
	return removeEntry(path, amazonQServersKey, serverName)
}
