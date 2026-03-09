package agent

// Scope controls which config file an AgentWriter targets.
type Scope int

const (
	// ScopeGlobal targets the user-level config (e.g. ~/.claude/settings.json).
	ScopeGlobal Scope = iota
	// ScopeProject targets a project-local config in the current working directory.
	ScopeProject
	// ScopeManaged targets the enterprise-managed layer (may require elevated permissions).
	ScopeManaged
)

// MCPEntry describes a single MCP server entry to be written into an agent config.
type MCPEntry struct {
	Type    string            // "sse" or "stdio"
	URL     string            // for type=sse
	Headers map[string]string // e.g. {"Authorization": "Bearer ..."}
}

// AgentWriter reads and writes MCP server configuration for a specific agent tool.
type AgentWriter interface {
	// Name returns the canonical tool name (e.g. "claude-code", "cursor").
	Name() string
	// Detect returns true if the tool appears to be installed on this machine.
	Detect() bool
	// Write merges the given MCPEntry under the given server name into the config file
	// selected by scope, creating the file (and parent directories) as needed.
	Write(scope Scope, serverName string, cfg MCPEntry) error
	// Read returns all MCP server entries currently present in the config file
	// selected by scope. Returns an empty map (not an error) when the file does
	// not exist.
	Read(scope Scope) (map[string]MCPEntry, error)
	// Remove deletes the named server entry from the config file selected by scope.
	// It is not an error if the entry does not exist.
	Remove(scope Scope, serverName string) error
}
