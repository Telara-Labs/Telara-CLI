package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	catalog "gitlab.com/telara-labs/telara-utilities/go/integrations/catalog"
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

// engineRecord is one resource emitted by the discover:file engine loop
// (schema doc §7's `emit(kind=..., id=..., fields=...)` step), before the
// mcp_server_deployment ones are folded into ConfigScanResult.Servers.
type engineRecord struct {
	Kind   string
	ID     string
	Fields map[string]string
	// Server is populated only for Kind == "mcp_server_deployment". Its
	// derivation deliberately does NOT go through the generic Fields
	// extraction below — see the privacy.go note in scanCatalogSource.
	Server *DiscoveredServer
}

// readConfigFile is the single low-level file read the engine loop performs
// per candidate config path. It is a package variable (not a hardcoded
// os.ReadFile call) purely so tests can wrap it to count invocations — see
// TestOneConfigFileYieldsClientConfigAndServers's read-count assertion.
var readConfigFile = os.ReadFile

// Scan scans one client/scope pair by name, matching it against the
// discover:file sources the embedded catalog declares (see catalog.go).
func Scan(clientFamily, scope string) ConfigScanResult {
	sources, err := fileSources()
	if err != nil {
		return ConfigScanResult{
			ClientFamily: clientFamily,
			Scope:        scope,
			PathClass:    "unsupported",
			Status:       ScanUnsupported,
			ErrorClass:   "catalog_load_failed",
		}
	}
	for _, src := range sources {
		if src.ClientID == clientFamily && src.Scope == scope {
			return scanCatalogSource(src)
		}
	}
	return ConfigScanResult{
		ClientFamily: clientFamily,
		Scope:        scope,
		PathClass:    "unsupported",
		Status:       ScanUnsupported,
		ErrorClass:   "unsupported",
	}
}

// ScanAll scans every discover:file source the catalog declares, preserving
// absence and failure results.
func ScanAll() []ConfigScanResult {
	sources, err := fileSources()
	if err != nil {
		return []ConfigScanResult{{
			ClientFamily: "catalog",
			Scope:        "all",
			PathClass:    "unsupported",
			Status:       ScanUnsupported,
			ErrorClass:   "catalog_load_failed",
		}}
	}
	results := make([]ConfigScanResult, 0, len(sources))
	for _, src := range sources {
		results = append(results, scanCatalogSource(src))
	}
	return results
}

// scanCatalogSource executes the schema doc's §7 engine loop body for one
// discover:file source: ONE read against the first present candidate in
// config_paths, then iterate every declared resource against that single
// parsed document.
func scanCatalogSource(src catalogFileSource) ConfigScanResult {
	result := ConfigScanResult{
		ClientFamily: src.ClientID,
		Scope:        src.Scope,
		Servers:      []DiscoveredServer{},
	}

	if len(src.ConfigPaths) == 0 {
		result.Status = ScanUnsupported
		result.ErrorClass = "no_config_paths"
		return result
	}

	path, data, format, readErr := readFirstPresentConfigPath(src.ConfigPaths)
	result.PathClass = PathClass(path)

	if readErr != nil {
		switch {
		case errors.Is(readErr, fs.ErrNotExist):
			result.Status = ScanFileAbsent
		case errors.Is(readErr, fs.ErrPermission):
			result.Status = ScanPermissionDenied
			result.ErrorClass = "permission_denied"
		default:
			result.Status = ScanPermissionDenied
			result.ErrorClass = "read_failed"
		}
		return result
	}

	top, err := parseConfig(data, format)
	if err != nil {
		result.Status = ScanParseFailed
		result.ErrorClass = "parse_error"
		return result
	}

	// "This is the case that needs multi-kind": one file read, walked once,
	// yields BOTH the mcp_client_configuration resource (the file itself)
	// and one mcp_server_deployment resource per entry under mcpServers —
	// schema doc §6a.
	for _, res := range src.Resources {
		records, dropped := walkResource(res, top, src, path)
		result.DroppedRecords += dropped
		result.records = append(result.records, records...)
		if res.Kind == "mcp_server_deployment" {
			for _, rec := range records {
				if rec.Server != nil {
					result.Servers = append(result.Servers, *rec.Server)
				}
			}
		}
	}

	result.Status = ScanOK
	return result
}

// walkResource carves one resource's record(s) out of one already-parsed
// config document, per schema doc §5/§7:
//
//   - res.ItemsPath == ""  → the resource IS the source's own record (the
//     config file itself — e.g. mcp_client_configuration).
//   - res.ItemsPath != ""  → a nested map within the document holds this
//     resource's records, keyed by name (e.g. mcpServers).
//
// mcp_server_deployment is special-cased to derive its DiscoveredServer via
// discoverServer() (transport normalization, credential classification,
// endpoint-host and command-identity redaction) rather than the generic
// Fields extraction below. That logic is privacy.go's, and privacy.go stays
// hand-written Go: what may be collected from an employee's machine is a
// policy decision, not a vendor fact, and must never become editable by
// adding a catalog `fields:`/`value_map:` row. The catalog only tells this
// engine WHERE to look (items_path, id_field) — never how to classify what
// it finds there.
func walkResource(res catalog.AIEstateResourceSpec, top map[string]interface{}, src catalogFileSource, path string) (records []engineRecord, dropped int) {
	if res.ItemsPath == "" {
		get := pseudoFieldResolver(top, "", src, path)
		id, ok := resolveIdentity(res, get)
		if !ok {
			return nil, 1
		}
		return []engineRecord{{
			Kind:   res.Kind,
			ID:     id,
			Fields: extractFields(res, top, get),
		}}, 0
	}

	items, ok := mapValue(top, res.ItemsPath)
	if !ok {
		// Nothing under this path in THIS file — not an error, zero records
		// from this resource (e.g. a config with no mcpServers key at all).
		return nil, 0
	}

	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		entry, _ := mapValue(items, key)
		get := pseudoFieldResolver(entry, key, src, path)
		id, ok := resolveIdentity(res, get)
		if !ok {
			dropped++
			continue
		}
		rec := engineRecord{Kind: res.Kind, ID: id, Fields: extractFields(res, entry, get)}
		if res.Kind == "mcp_server_deployment" {
			// rec.Fields (this resource's catalog `fields:`/`value_map:`,
			// e.g. transport: {"": stdio}) IS computed above — it is not
			// silently dropped at parse time — but it deliberately does NOT
			// feed rec.Server below. discoverServer independently derives
			// transport/credential/endpoint/command from the same raw entry
			// via privacy.go, which is the one true source for anything
			// privacy-sensitive. rec.Fields stays on the record for
			// introspection (see TestOneConfigFileYieldsClientConfigAndServers)
			// but is not itself wire-facing.
			server := discoverServer(key, entry)
			rec.Server = &server
		}
		records = append(records, rec)
	}
	return records, dropped
}

// pseudoFieldResolver returns a field getter honouring the small closed
// vocabulary of "_"-prefixed pseudo-fields the schema doc's worked examples
// use (§6a: id_field: _path, id_field: _key, fields: {client: _client_id,
// scope: _scope}), falling back to a literal top-level lookup in item for
// anything else.
func pseudoFieldResolver(item map[string]interface{}, key string, src catalogFileSource, path string) func(string) string {
	return func(field string) string {
		switch field {
		case "_path":
			return path
		case "_key":
			return key
		case "_client_id":
			return src.ClientID
		case "_scope":
			return src.Scope
		default:
			return stringValue(item, field)
		}
	}
}

// resolveIdentity implements schema doc §5's id_field / id_fallback_fields /
// id_fallback_prefix contract: try id_field, then each fallback field in
// order (prefixed so a fallback identity can never collide with a primary
// one), and report failure — never inventing an identity — when nothing
// resolves. Per §7 property 3, the caller must count every such failure
// against coverage rather than dropping it silently.
func resolveIdentity(res catalog.AIEstateResourceSpec, get func(field string) string) (id string, resolved bool) {
	if res.IDField != "" {
		if v := get(res.IDField); v != "" {
			return v, true
		}
	}
	for _, fallback := range res.IDFallbackFields {
		if v := get(fallback); v != "" {
			return res.IDFallbackPrefix + v, true
		}
	}
	return "", false
}

// extractFields implements schema doc §5's multi-path fields contract: "A
// destination may name several paths — tried in order for scalars,
// concatenated for lists", then applies value_map (keyed by destination,
// AIEstateValueMapDefault-less here since none of today's file sources need
// a default — see catalog.go's value_map handling for the two kinds that
// exercise this).
func extractFields(res catalog.AIEstateResourceSpec, item map[string]interface{}, get func(string) string) map[string]string {
	if len(res.Fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(res.Fields))
	for dest, paths := range res.Fields {
		value := extractField(paths, get, func(p string) []string { return stringSliceValue(item, p) })
		out[dest] = applyValueMap(res.ValueMap, dest, value)
	}
	return out
}

// extractField is the multi-path resolver itself: every candidate path that
// resolves to a list contributes its items (concatenated, comma-joined,
// across all such candidates); otherwise the first candidate resolving to a
// non-empty scalar wins.
func extractField(paths []string, get func(string) string, getList func(string) []string) string {
	var lists []string
	for _, p := range paths {
		if items := getList(p); len(items) > 0 {
			lists = append(lists, items...)
			continue
		}
		if lists == nil {
			if v := get(p); v != "" {
				return v
			}
		}
	}
	if len(lists) > 0 {
		return strings.Join(lists, ",")
	}
	return ""
}

// applyValueMap translates provider vocabulary into canonical labels for one
// destination field, per schema doc §5's value_map. An unlisted value passes
// through unchanged.
func applyValueMap(valueMap map[string]map[string]string, dest, value string) string {
	table, ok := valueMap[dest]
	if !ok {
		return value
	}
	if mapped, ok := table[value]; ok {
		return mapped
	}
	return value
}

// resolveConfigPath expands a catalog config_paths entry into a filesystem
// path: "~/" expands against the user's home directory, an absolute path
// (POSIX or a Windows drive-letter path, even when evaluated on a different
// OS — the managed scope lists all three platforms' candidates in one
// source) is used as-is, and anything else is resolved against the current
// working directory (project scope).
func resolveConfigPath(raw string) string {
	if strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return raw
		}
		return filepath.Join(home, raw[2:])
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	if len(raw) >= 3 && raw[1] == ':' && (raw[2] == '\\' || raw[2] == '/') {
		// A Windows drive-letter path evaluated on darwin/linux (managed
		// scope's Windows candidate) — filepath.IsAbs only recognises this
		// shape on GOOS=windows.
		return raw
	}
	cwd, err := os.Getwd()
	if err != nil {
		return raw
	}
	return filepath.Join(cwd, raw)
}

// formatFor infers the config format from the resolved path's extension,
// replacing the old per-client format table: every file source's format is
// fully determined by its own path, never by which vendor declared it.
func formatFor(path string) configFormat {
	if strings.HasSuffix(strings.ToLower(path), ".toml") {
		return configTOML
	}
	return configJSON
}

// readFirstPresentConfigPath tries each candidate in order and returns the
// first one that exists, reading it exactly once. This replaces the old
// runtime.GOOS switch claudeManagedPath() used to pick one managed path:
// the catalog now lists all platform candidates on one source, and since
// each is an OS-namespaced absolute path (/Library/..., /etc/...,
// C:\ProgramData\...), at most one can plausibly exist on any given machine
// — no GOOS branch is needed to choose among them. A real (non-ErrNotExist)
// failure on any candidate is returned immediately rather than masked by
// trying the next one.
func readFirstPresentConfigPath(rawPaths []string) (resolvedPath string, data []byte, format configFormat, readErr error) {
	for _, raw := range rawPaths {
		resolved := resolveConfigPath(raw)
		resolvedPath = resolved
		d, err := readConfigFile(resolved)
		if err == nil {
			return resolved, d, formatFor(resolved), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return resolved, nil, formatFor(resolved), err
		}
		readErr = err
	}
	return resolvedPath, nil, "", readErr
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
