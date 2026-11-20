package mcpquarantine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// Device escrow (TENG-3921, decision 3). Block
// telara-documentation/architecture/estate-remediation.md ("Device escrow").
//
// The copy kept in Telara never carries a credential: every env and header
// value is emptied, the URL query is dropped, and an argument that looks like a
// secret (a --token style flag's value, or KEY=VALUE where the key names a
// secret) is emptied. The emptied paths are listed, because each needs a new
// credential when the entry comes back from escrow.

var secretWord = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[-_]?key|apikey|auth|credential|private[-_]?key|bearer)`)

// StripEntry empties every credential value in one MCP entry.
func StripEntry(raw json.RawMessage) (json.RawMessage, []string, error) {
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, nil, fmt.Errorf("the entry is not a JSON object: %w", err)
	}
	var stripped []string
	for _, field := range []string{"env", "headers", "http_headers"} {
		m, ok := entry[field].(map[string]any)
		if !ok {
			continue
		}
		for k := range m {
			m[k] = ""
			stripped = append(stripped, field+"."+k)
		}
	}
	if u, ok := entry["url"].(string); ok {
		if parsed, err := url.Parse(u); err == nil && (parsed.RawQuery != "" || parsed.User != nil) {
			parsed.RawQuery, parsed.User = "", nil
			entry["url"] = parsed.String()
			stripped = append(stripped, "url.query")
		}
	}
	if args, ok := entry["args"].([]any); ok {
		for i := 0; i < len(args); i++ {
			s, ok := args[i].(string)
			if !ok {
				continue
			}
			if k, _, found := strings.Cut(s, "="); found && secretWord.MatchString(k) {
				args[i] = k + "="
				stripped = append(stripped, fmt.Sprintf("args[%d]", i))
				continue
			}
			if strings.HasPrefix(s, "-") && secretWord.MatchString(s) && i+1 < len(args) {
				args[i+1] = ""
				stripped = append(stripped, fmt.Sprintf("args[%d]", i+1))
				i++
			}
		}
	}
	out, err := json.Marshal(entry)
	return out, stripped, err
}

// HasRecord reports whether a local quarantine record holds this entry.
func HasRecord(homeDir, clientID, key string) bool {
	entries, err := os.ReadDir(Root(homeDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(Root(homeDir), e.Name()))
		if err != nil {
			continue
		}
		var rec Record
		if json.Unmarshal(raw, &rec) == nil && rec.ClientID == clientID && rec.Key == key {
			return true
		}
	}
	return false
}

// ReadRecord loads one local quarantine record.
func ReadRecord(path string) (Record, error) {
	var rec Record
	raw, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(raw, &rec)
}

// RestoreFromEscrow puts an escrowed (credential-free) entry back into the
// client's user-level file. It never overwrites an entry added back by hand.
func RestoreFromEscrow(homeDir, clientID, itemsKey, key string, entry json.RawMessage) (Action, error) {
	act := Action{ClientID: clientID}
	var target *discovery.MCPConfigFile
	for _, f := range discovery.QuarantinableMCPFiles(homeDir) {
		if f.ClientID == clientID && f.ItemsKey == itemsKey {
			f := f
			target = &f
			break
		}
	}
	if target == nil {
		return act, fmt.Errorf("no user-level %s MCP file to restore %s into", clientID, key)
	}
	act.Path = target.Path
	data, err := os.ReadFile(target.Path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("{}")
	} else if err != nil {
		return act, fmt.Errorf("read %s: %w", target.Path, err)
	}
	doc, err := parseObject(data)
	if err != nil {
		return act, fmt.Errorf("%s is not a JSON object: %w", target.Path, err)
	}
	items := &object{}
	if raw, ok := doc.get(itemsKey); ok {
		if items, err = parseObject(raw); err != nil {
			return act, fmt.Errorf("%s %s is not an object", target.Path, itemsKey)
		}
	}
	if _, exists := items.get(key); exists {
		act.Kind, act.Note = "conflict", "a server with this name was added back by hand; the escrowed entry was not merged"
		return act, nil
	}
	items.set(key, entry)
	doc.set(itemsKey, items.encode())
	if err := os.MkdirAll(filepath.Dir(target.Path), 0o700); err != nil {
		return act, err
	}
	if err := writeFileAtomic(target.Path, doc.encode()); err != nil {
		return act, fmt.Errorf("rewrite %s: %w", target.Path, err)
	}
	act.Kind = "reinstated_from_escrow"
	return act, nil
}
