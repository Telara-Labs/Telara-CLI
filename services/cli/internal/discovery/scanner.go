package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
	ScopeManaged = "managed"

	// Skill roots are their own collection boundary, not the MCP-config scope
	// under a different name. Reusing ScopeGlobal would make two different
	// scans collide on one scope key, double-counting coverage and letting an
	// absent skills directory license a tombstone against MCP configuration.
	ScopeGlobalSkills  = "global-skills"
	ScopeProjectSkills = "project-skills"

	ClientClaudeCode = "claude-code"
	ClientCursor     = "cursor"
	ClientWindsurf   = "windsurf"
	ClientVSCode     = "vscode"
	ClientCodex      = "codex"
	ClientGemini     = "gemini"
	ClientAmazonQ    = "amazon-q"
)

type configFormat string

const (
	configJSON configFormat = "json"
	configTOML configFormat = "toml"
)

type scanSpec struct {
	clientFamily string
	scope        string
	path         string
	serversKey   string
	format       configFormat
}

// Scan scans one supported client family and scope.
func Scan(clientFamily, scope string) ConfigScanResult {
	spec, ok := specFor(clientFamily, scope)
	if !ok {
		return ConfigScanResult{
			ClientFamily: clientFamily,
			Scope:        scope,
			PathClass:    "unsupported",
			Status:       ScanUnsupported,
			ErrorClass:   "unsupported",
		}
	}
	return scanSpecFile(spec)
}

// ScanAll scans every supported client/scope pair and preserves absence and failure results.
func ScanAll() []ConfigScanResult {
	specs := allSpecs()
	results := make([]ConfigScanResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, scanSpecFile(spec))
	}
	return results
}

func scanSpecFile(spec scanSpec) ConfigScanResult {
	result := ConfigScanResult{
		ClientFamily: spec.clientFamily,
		Scope:        spec.scope,
		PathClass:    PathClass(spec.path),
		Servers:      []DiscoveredServer{},
	}

	data, err := os.ReadFile(spec.path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			result.Status = ScanFileAbsent
		case errors.Is(err, fs.ErrPermission):
			result.Status = ScanPermissionDenied
			result.ErrorClass = "permission_denied"
		default:
			result.Status = ScanPermissionDenied
			result.ErrorClass = "read_failed"
		}
		return result
	}

	top, err := parseConfig(data, spec.format)
	if err != nil {
		result.Status = ScanParseFailed
		result.ErrorClass = "parse_error"
		return result
	}

	servers, ok := mapValue(top, spec.serversKey)
	if !ok {
		result.Status = ScanOK
		return result
	}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, _ := mapValue(servers, name)
		result.Servers = append(result.Servers, discoverServer(name, entry))
	}
	result.Status = ScanOK
	return result
}

func parseConfig(data []byte, format configFormat) (map[string]interface{}, error) {
	var top map[string]interface{}
	switch format {
	case configJSON:
		if err := json.Unmarshal(data, &top); err != nil {
			return nil, err
		}
	case configTOML:
		if err := toml.Unmarshal(data, &top); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported config format %q", format)
	}
	if top == nil {
		return map[string]interface{}{}, nil
	}
	return top, nil
}

func discoverServer(name string, entry map[string]interface{}) DiscoveredServer {
	command := stringValue(entry, "command")
	args := stringSliceValue(entry, "args")
	rawURL := stringValue(entry, "url")
	headers := stringMapValue(entry, "headers")
	for key, value := range stringMapValue(entry, "http_headers") {
		headers[key] = value
	}
	env := stringMapValue(entry, "env")
	credentialClass, credentialHint := ClassifyCredential(headers, env)

	server := DiscoveredServer{
		ServerName:      name,
		Transport:       normalizeTransport(stringValue(entry, "type"), command, rawURL),
		RawArgCount:     len(args),
		CredentialClass: credentialClass,
		CredentialHint:  credentialHint,
	}
	if rawURL != "" {
		server.EndpointHost = NormalizeEndpointHost(rawURL)
	}
	if command != "" {
		server.CommandIdentity = NormalizeCommandIdentity(command, args)
	}
	return server
}

func normalizeTransport(rawType, command, rawURL string) string {
	switch strings.ToLower(strings.TrimSpace(rawType)) {
	case TransportStdio:
		return TransportStdio
	case TransportHTTP, "streamable-http":
		return TransportHTTP
	case TransportSSE:
		return TransportSSE
	}
	if command != "" {
		return TransportStdio
	}
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err == nil && strings.Contains(strings.ToLower(parsed.Path), "sse") {
			return TransportSSE
		}
		return TransportHTTP
	}
	return TransportUnknown
}

func mapValue(parent map[string]interface{}, key string) (map[string]interface{}, bool) {
	raw, ok := parent[key]
	if !ok || raw == nil {
		return nil, false
	}
	value, ok := raw.(map[string]interface{})
	return value, ok
}

func stringValue(parent map[string]interface{}, key string) string {
	if parent == nil {
		return ""
	}
	value, _ := parent[key].(string)
	return value
}

func stringSliceValue(parent map[string]interface{}, key string) []string {
	if parent == nil {
		return nil
	}
	raw, ok := parent[key].([]interface{})
	if !ok {
		if typed, ok := parent[key].([]string); ok {
			return typed
		}
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			values = append(values, value)
		}
	}
	return values
}

func stringMapValue(parent map[string]interface{}, key string) map[string]string {
	values := make(map[string]string)
	if parent == nil {
		return values
	}
	raw, ok := parent[key].(map[string]interface{})
	if !ok {
		if typed, ok := parent[key].(map[string]string); ok {
			for k, v := range typed {
				values[k] = v
			}
		}
		return values
	}
	for k, v := range raw {
		if value, ok := v.(string); ok {
			values[k] = value
		}
	}
	return values
}

func specFor(clientFamily, scope string) (scanSpec, bool) {
	for _, spec := range allSpecs() {
		if spec.clientFamily == clientFamily && spec.scope == scope {
			return spec, true
		}
	}
	return scanSpec{}, false
}

func allSpecs() []scanSpec {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	return []scanSpec{
		{ClientClaudeCode, ScopeGlobal, filepath.Join(home, ".claude.json"), "mcpServers", configJSON},
		{ClientClaudeCode, ScopeProject, filepath.Join(cwd, ".mcp.json"), "mcpServers", configJSON},
		{ClientClaudeCode, ScopeManaged, claudeManagedPath(), "mcpServers", configJSON},
		{ClientCursor, ScopeGlobal, filepath.Join(home, ".cursor", "mcp.json"), "mcpServers", configJSON},
		{ClientCursor, ScopeProject, filepath.Join(cwd, ".cursor", "mcp.json"), "mcpServers", configJSON},
		{ClientWindsurf, ScopeGlobal, filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), "mcpServers", configJSON},
		{ClientWindsurf, ScopeProject, filepath.Join(cwd, ".windsurf", "mcp_config.json"), "mcpServers", configJSON},
		{ClientVSCode, ScopeProject, filepath.Join(cwd, ".vscode", "mcp.json"), "servers", configJSON},
		{ClientCodex, ScopeGlobal, filepath.Join(home, ".codex", "config.toml"), "mcp_servers", configTOML},
		{ClientCodex, ScopeProject, filepath.Join(cwd, ".codex", "config.toml"), "mcp_servers", configTOML},
		{ClientGemini, ScopeGlobal, filepath.Join(home, ".gemini", "settings.json"), "mcpServers", configJSON},
		{ClientGemini, ScopeProject, filepath.Join(cwd, ".gemini", "settings.json"), "mcpServers", configJSON},
		{ClientAmazonQ, ScopeGlobal, filepath.Join(home, ".aws", "amazonq", "mcp.json"), "mcpServers", configJSON},
		{ClientAmazonQ, ScopeProject, filepath.Join(cwd, ".amazonq", "mcp.json"), "mcpServers", configJSON},
	}
}

func claudeManagedPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-mcp.json"
	case "windows":
		return `C:\ProgramData\ClaudeCode\managed-mcp.json`
	default:
		return "/etc/claude-code/managed-mcp.json"
	}
}
