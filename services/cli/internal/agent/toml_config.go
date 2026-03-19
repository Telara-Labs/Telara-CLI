package agent

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// readTOMLConfig reads a TOML config file at path and returns the raw top-level map.
// If the file does not exist, an empty map is returned without error.
func readTOMLConfig(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]interface{}), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out map[string]interface{}
	if err := toml.Unmarshal(data, &out); err != nil {
		// Corrupted file — start fresh rather than propagating a parse error.
		return make(map[string]interface{}), nil
	}
	return out, nil
}

// writeTOMLConfig marshals cfg and writes it to path atomically via a temp file.
// Parent directories are created automatically.
func writeTOMLConfig(path string, cfg map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	data, err := toml.Marshal(cfg)
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

// tomlEntryToMap converts an MCPEntry to the TOML-serialisable map form.
// Codex uses "url" and "http_headers" field names.
func tomlEntryToMap(e MCPEntry) map[string]interface{} {
	m := make(map[string]interface{})
	if e.URL != "" {
		m["url"] = e.URL
	}
	if len(e.Headers) > 0 {
		headers := make(map[string]interface{}, len(e.Headers))
		for k, v := range e.Headers {
			headers[k] = v
		}
		m["http_headers"] = headers
	}
	return m
}

// tomlMapToEntry converts a raw TOML map back to an MCPEntry.
func tomlMapToEntry(raw interface{}) (MCPEntry, bool) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return MCPEntry{}, false
	}
	entry := MCPEntry{Type: "sse"}
	if u, ok := m["url"].(string); ok {
		entry.URL = u
	}
	if h, ok := m["http_headers"].(map[string]interface{}); ok {
		entry.Headers = make(map[string]string, len(h))
		for k, v := range h {
			if s, ok := v.(string); ok {
				entry.Headers[k] = s
			}
		}
	}
	return entry, true
}

// writeTOMLEntry writes a single MCPEntry under serversKey → serverName.
func writeTOMLEntry(path, serversKey, serverName string, cfg MCPEntry) error {
	top, err := readTOMLConfig(path)
	if err != nil {
		return err
	}
	servers, _ := top[serversKey].(map[string]interface{})
	if servers == nil {
		servers = make(map[string]interface{})
	}
	servers[serverName] = tomlEntryToMap(cfg)
	top[serversKey] = servers
	return writeTOMLConfig(path, top)
}

// readTOMLEntries reads all MCPEntry values under serversKey.
func readTOMLEntries(path, serversKey string) (map[string]MCPEntry, error) {
	top, err := readTOMLConfig(path)
	if err != nil {
		return nil, err
	}
	servers, _ := top[serversKey].(map[string]interface{})
	if servers == nil {
		return make(map[string]MCPEntry), nil
	}
	result := make(map[string]MCPEntry, len(servers))
	for name, raw := range servers {
		if entry, ok := tomlMapToEntry(raw); ok {
			result[name] = entry
		}
	}
	return result, nil
}

// removeTOMLEntry removes a server entry from the TOML config.
func removeTOMLEntry(path, serversKey, serverName string) error {
	top, err := readTOMLConfig(path)
	if err != nil {
		return err
	}
	servers, _ := top[serversKey].(map[string]interface{})
	if servers == nil {
		return nil
	}
	if _, exists := servers[serverName]; !exists {
		return nil
	}
	delete(servers, serverName)
	top[serversKey] = servers
	return writeTOMLConfig(path, top)
}

// setTOMLServerEntryField sets a field on a server entry within the TOML config.
func setTOMLServerEntryField(path, serversKey, serverName, fieldName string, value interface{}) error {
	top, err := readTOMLConfig(path)
	if err != nil {
		return err
	}
	servers, _ := top[serversKey].(map[string]interface{})
	if servers == nil {
		return nil
	}
	entry, ok := servers[serverName].(map[string]interface{})
	if !ok || entry == nil {
		return nil
	}
	entry[fieldName] = value
	servers[serverName] = entry
	top[serversKey] = servers
	return writeTOMLConfig(path, top)
}

// removeTOMLServerEntryField removes a field from a server entry in the TOML config.
func removeTOMLServerEntryField(path, serversKey, serverName, fieldName string) error {
	top, err := readTOMLConfig(path)
	if err != nil {
		return err
	}
	servers, _ := top[serversKey].(map[string]interface{})
	if servers == nil {
		return nil
	}
	entry, ok := servers[serverName].(map[string]interface{})
	if !ok || entry == nil {
		return nil
	}
	delete(entry, fieldName)
	servers[serverName] = entry
	top[serversKey] = servers
	return writeTOMLConfig(path, top)
}
