package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readJSONConfig reads a JSON config file at path and returns the raw top-level map.
// If the file does not exist, an empty map is returned without error.
func readJSONConfig(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]interface{}), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		// Corrupted file — start fresh rather than propagating a parse error.
		return make(map[string]interface{}), nil
	}
	return out, nil
}

// writeJSONConfig marshals cfg and writes it to path atomically via a temp file.
// Parent directories are created automatically.
func writeJSONConfig(path string, cfg map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// getServersMap extracts the servers sub-map from a top-level config map using
// the given key (e.g. "mcpServers" or "servers"). If the key is absent or has
// the wrong type, an empty map is returned.
func getServersMap(cfg map[string]interface{}, key string) map[string]interface{} {
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return make(map[string]interface{})
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return make(map[string]interface{})
	}
	return m
}

// entryToMap converts an MCPEntry to the JSON-serialisable map form.
func entryToMap(e MCPEntry) map[string]interface{} {
	m := map[string]interface{}{
		"type": e.Type,
		"url":  e.URL,
	}
	if len(e.Headers) > 0 {
		headers := make(map[string]interface{}, len(e.Headers))
		for k, v := range e.Headers {
			headers[k] = v
		}
		m["headers"] = headers
	}
	return m
}

// mapToEntry converts a raw JSON map back to an MCPEntry.
func mapToEntry(raw interface{}) (MCPEntry, bool) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return MCPEntry{}, false
	}
	entry := MCPEntry{}
	if t, ok := m["type"].(string); ok {
		entry.Type = t
	}
	if u, ok := m["url"].(string); ok {
		entry.URL = u
	}
	if h, ok := m["headers"].(map[string]interface{}); ok {
		entry.Headers = make(map[string]string, len(h))
		for k, v := range h {
			if s, ok := v.(string); ok {
				entry.Headers[k] = s
			}
		}
	}
	return entry, true
}

// writeEntry is the shared implementation for Write across all writers that use
// a single JSON file with a map of server entries under serversKey.
func writeEntry(path, serversKey, serverName string, cfg MCPEntry) error {
	top, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	servers := getServersMap(top, serversKey)
	servers[serverName] = entryToMap(cfg)
	top[serversKey] = servers
	return writeJSONConfig(path, top)
}

// readEntries is the shared implementation for Read.
func readEntries(path, serversKey string) (map[string]MCPEntry, error) {
	top, err := readJSONConfig(path)
	if err != nil {
		return nil, err
	}
	servers := getServersMap(top, serversKey)
	result := make(map[string]MCPEntry, len(servers))
	for name, raw := range servers {
		if entry, ok := mapToEntry(raw); ok {
			result[name] = entry
		}
	}
	return result, nil
}

// removeEntry is the shared implementation for Remove.
func removeEntry(path, serversKey, serverName string) error {
	top, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	servers := getServersMap(top, serversKey)
	if _, exists := servers[serverName]; !exists {
		return nil
	}
	delete(servers, serverName)
	top[serversKey] = servers
	return writeJSONConfig(path, top)
}
