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
	Type    string            // "sse", "http", or "stdio"
	URL     string            // for type=sse or type=http
	Headers map[string]string // e.g. {"Authorization": "Bearer ..."}
}

// PermissionWriter is an optional interface that AgentWriters can implement
// to manage tool auto-approval permissions. Writers that support it (e.g. Claude Code)
// can auto-approve MCP tools so users aren't double-prompted.
type PermissionWriter interface {
	WritePermissions(scope Scope, serverName string, tools []string) error
	RemovePermissions(scope Scope, serverName string) error
}

// PlatformToolNames is the OFFLINE FALLBACK for clients that auto-approve by
// explicit tool name (Cursor, Windsurf, Codex). The live list comes from the
// server via FetchToolNames; this is used only when it cannot be reached.
//
// It is a copy of a list the gateway owns, so it is pinned by
// TestPlatformToolNamesMatchesGatewaySurface — if that test fails, the gateway
// changed and this list must follow.
func PlatformToolNames() []string {
	return []string{
		// Knowledge
		"telara_knowledge_search",
		"telara_knowledge_traverse",
		"telara_knowledge_get_context",
		"telara_knowledge_impact",
		"telara_knowledge_annotate",
		"telara_knowledge_link",
		"telara_knowledge_timeline",
		"telara_code_call_hierarchy",
		"telara_browser_extract",
		// Archive — archive_batch_read was folded into archive_read(file_paths).
		"telara_archive_read",
		"telara_archive_ls",
		"telara_archive_search",
		// Tasks
		"telara_task_list",
		"telara_task_create",
		"telara_task_resume",
		"telara_task_checkpoint",
		"telara_task_complete",
		"telara_task_pause",
		// Action execution and on-demand discovery
		"telara_execute_action",
		"telara_tool_search",
		"telara_tool_describe",
	}
}

// AgentWriter reads and writes MCP server configuration for a specific agent tool.
type AgentWriter interface {
	// Name returns the canonical tool name (e.g. "claude-code", "cursor").
	Name() string
	// Detect returns true if the tool appears to be installed on this machine.
	Detect() bool
	// ConfigPath returns the path of the config file for the given scope.
	// For ScopeProject, the path is relative to the current working directory.
	ConfigPath(scope Scope) (string, error)
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
