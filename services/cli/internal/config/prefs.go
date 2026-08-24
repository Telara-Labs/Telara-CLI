package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultAPIURL = "https://api.telara.dev"

// NormalizeAPIBaseURL trims whitespace and trailing slashes and fixes common double-scheme mistakes
// (e.g. https://https://host) so MCP URLs built as base+"/v1/mcp/sse" stay valid.
func NormalizeAPIBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	for strings.HasPrefix(s, "https://https://") {
		s = strings.TrimPrefix(s, "https://")
	}
	for strings.HasPrefix(s, "http://https://") {
		s = strings.TrimPrefix(s, "http://")
	}
	for strings.HasPrefix(s, "https://http://") {
		s = strings.TrimPrefix(s, "https://")
	}
	return strings.TrimSuffix(s, "/")
}

// Prefs holds user-level CLI preferences persisted to config.json.
//
// An `auto_rotate` field lived here from the first commit but was never read
// or written by any released CLI (omitempty + no writer means it never reached
// disk), so it was removed in TENG-2353. A hand-added `auto_rotate` key in an
// existing config.json is ignored on load, as encoding/json drops unknown keys.
type Prefs struct {
	ActiveContext string `json:"active_context,omitempty"`
	APIURL        string `json:"api_url,omitempty"`
}

// DefaultPrefs returns a Prefs struct with default values applied.
func DefaultPrefs() *Prefs {
	return &Prefs{
		APIURL: defaultAPIURL,
	}
}

// Load reads config.json from the config directory and returns parsed Prefs.
// If the file does not exist, default values are returned without error.
func Load() (*Prefs, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, fmt.Errorf("config dir: %w", err)
	}

	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultPrefs(), nil
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}

	p := DefaultPrefs()
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Ensure APIURL always has a value
	if p.APIURL == "" {
		p.APIURL = defaultAPIURL
	}
	p.APIURL = NormalizeAPIBaseURL(p.APIURL)

	// Auto-migrate stale www.telara.dev URLs left over from before the static export migration.
	// www.telera.dev is now a CDN-hosted static site and cannot proxy /v1/* requests.
	if p.APIURL == "https://www.telara.dev" {
		p.APIURL = defaultAPIURL
		_ = Save(p) // best-effort; ignore error so login still works
	}

	return p, nil
}

// Save writes the given Prefs to config.json atomically via a temp file + rename.
func Save(p *Prefs) error {
	dir, err := ConfigDir()
	if err != nil {
		return fmt.Errorf("config dir: %w", err)
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal prefs: %w", err)
	}

	path := filepath.Join(dir, "config.json")
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename config file: %w", err)
	}

	return nil
}
