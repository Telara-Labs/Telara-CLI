// Package mcpquarantine takes MCP servers a tenant blocked out of this
// device's client configuration and puts them back when the block is lifted
// (TENG-3921). Block telara-documentation/architecture/estate-remediation.md.
//
// The block itself is the gateway's: the tenant governance setting
// mcp_server_blocklist (one JSON array of server names), which a remediation
// writes with the mcp_server_block procedure. This package is the device side
// of the same decision, so a blocked server also stops loading in local
// clients. It is best effort: it runs when the CLI scans, edits only user-level
// files, and never touches project or managed configuration.
//
// Never delete. The removed entry is written to a quarantine record before the
// client file is changed, and the record is what reinstates it. A record holds
// the entry as it was, credentials included, so it is written 0600 under
// ~/.telara, outside every client load path.
package mcpquarantine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// BlocklistSettingKey is the tenant governance setting holding blocked names.
const BlocklistSettingKey = "mcp_server_blocklist"

// Root is where quarantined MCP entries are kept.
func Root(homeDir string) string {
	return filepath.Join(homeDir, ".telara", "quarantine", "mcp")
}

// Record is one quarantined entry.
type Record struct {
	Server        string          `json:"server"`
	Key           string          `json:"key"`
	ClientID      string          `json:"client_id"`
	Path          string          `json:"path"`
	ItemsKey      string          `json:"items_key"`
	Entry         json.RawMessage `json:"entry"`
	QuarantinedAt time.Time       `json:"quarantined_at"`
}

// Action reports one change, or one entry left alone and why.
type Action struct {
	Server   string `json:"server"`
	ClientID string `json:"client_id"`
	Path     string `json:"path"`
	Kind     string `json:"kind"` // quarantined | reinstated | conflict
	Record   string `json:"record,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ParseBlocklist reads the governance setting value (a JSON array of names).
func ParseBlocklist(value string) (map[string]bool, error) {
	value = strings.TrimSpace(value)
	out := map[string]bool{}
	if value == "" {
		return out, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(value), &names); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of names: %w", BlocklistSettingKey, err)
	}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out, nil
}

// blockedName is the blocklist name a local key matches: the gateway names a
// server in lowercase, so the local key is compared case-insensitively and
// otherwise exactly. A key that differs in any other way is not touched.
func blockedName(key string, blocked map[string]bool) (string, bool) {
	name := strings.ToLower(key)
	return name, blocked[name]
}

// Sync quarantines every blocked server in the user's MCP files and reinstates
// every quarantined server that is no longer blocked. It returns what it did
// even when it also returns an error.
func Sync(homeDir string, blocked map[string]bool, now time.Time) ([]Action, error) {
	var actions []Action
	var errs []error
	for _, f := range discovery.QuarantinableMCPFiles(homeDir) {
		a, err := quarantineFile(homeDir, f, blocked, now)
		actions = append(actions, a...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	a, err := reinstate(homeDir, blocked)
	actions = append(actions, a...)
	if err != nil {
		errs = append(errs, err)
	}
	return actions, errors.Join(errs...)
}

func quarantineFile(homeDir string, f discovery.MCPConfigFile, blocked map[string]bool, now time.Time) ([]Action, error) {
	if len(blocked) == 0 {
		return nil, nil
	}
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Path, err)
	}
	doc, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", f.Path, err)
	}
	itemsRaw, ok := doc.get(f.ItemsKey)
	if !ok {
		return nil, nil
	}
	items, err := parseObject(itemsRaw)
	if err != nil {
		return nil, nil // not an object of servers: discovery reports it, nothing to remove
	}
	var actions []Action
	changed := false
	for _, key := range items.keys() {
		name, hit := blockedName(key, blocked)
		if !hit {
			continue
		}
		entry, _ := items.get(key)
		rec := Record{Server: name, Key: key, ClientID: f.ClientID, Path: f.Path, ItemsKey: f.ItemsKey, Entry: entry, QuarantinedAt: now.UTC()}
		recPath, err := writeRecord(homeDir, rec)
		if err != nil {
			return actions, err
		}
		items.remove(key)
		changed = true
		actions = append(actions, Action{Server: name, ClientID: f.ClientID, Path: f.Path, Kind: "quarantined", Record: recPath})
	}
	if !changed {
		return actions, nil
	}
	doc.set(f.ItemsKey, items.encode())
	if err := writeFileAtomic(f.Path, doc.encode()); err != nil {
		return actions, fmt.Errorf("rewrite %s (the quarantine records are kept and reinstate it): %w", f.Path, err)
	}
	return actions, nil
}

func reinstate(homeDir string, blocked map[string]bool) ([]Action, error) {
	entries, err := os.ReadDir(Root(homeDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var actions []Action
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		recPath := filepath.Join(Root(homeDir), e.Name())
		raw, err := os.ReadFile(recPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			errs = append(errs, fmt.Errorf("quarantine record %s: %w", recPath, err))
			continue
		}
		if blocked[rec.Server] {
			continue
		}
		a, err := reinstateOne(rec, recPath)
		if a != nil {
			actions = append(actions, *a)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return actions, errors.Join(errs...)
}

func reinstateOne(rec Record, recPath string) (*Action, error) {
	act := &Action{Server: rec.Server, ClientID: rec.ClientID, Path: rec.Path, Record: recPath}
	data, err := os.ReadFile(rec.Path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("{}")
	} else if err != nil {
		return nil, fmt.Errorf("read %s: %w", rec.Path, err)
	}
	doc, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object; %s stays quarantined: %w", rec.Path, rec.Server, err)
	}
	items := &object{}
	if raw, ok := doc.get(rec.ItemsKey); ok {
		if items, err = parseObject(raw); err != nil {
			return nil, fmt.Errorf("%s %s is not an object; %s stays quarantined", rec.Path, rec.ItemsKey, rec.Server)
		}
	}
	if _, exists := items.get(rec.Key); exists {
		act.Kind, act.Note = "conflict", "a server with this name was added back by hand; the quarantined entry is kept, not merged"
		return act, nil
	}
	items.set(rec.Key, rec.Entry)
	doc.set(rec.ItemsKey, items.encode())
	if err := writeFileAtomic(rec.Path, doc.encode()); err != nil {
		return nil, fmt.Errorf("rewrite %s: %w", rec.Path, err)
	}
	if err := os.Remove(recPath); err != nil {
		return nil, fmt.Errorf("reinstated %s but could not remove %s: %w", rec.Server, recPath, err)
	}
	act.Kind = "reinstated"
	return act, nil
}

func writeRecord(homeDir string, rec Record) (string, error) {
	if err := os.MkdirAll(Root(homeDir), 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rec.Path + "\x00" + rec.ItemsKey + "\x00" + rec.Key))
	path := filepath.Join(Root(homeDir), rec.Server+"-"+hex.EncodeToString(sum[:6])+".json")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("a quarantine record for %s from %s already exists (%s); the entry was left in place", rec.Server, rec.Path, path)
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	return path, writeFileAtomic(path, body)
}

func writeFileAtomic(path string, body []byte) error {
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".telara-quarantine-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// object is a JSON object that keeps its key order and raw values, so a
// client's file changes only by the one entry removed or put back.
type object struct {
	order  []string
	values map[string]json.RawMessage
}

func parseObject(data []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	o := &object{values: map[string]json.RawMessage{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, dup := o.values[key]; !dup {
			o.order = append(o.order, key)
		}
		o.values[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after the object")
	}
	return o, nil
}

func (o *object) keys() []string { return append([]string(nil), o.order...) }

func (o *object) get(k string) (json.RawMessage, bool) {
	v, ok := o.values[k]
	return v, ok
}

func (o *object) set(k string, v json.RawMessage) {
	if o.values == nil {
		o.values = map[string]json.RawMessage{}
	}
	if _, ok := o.values[k]; !ok {
		o.order = append(o.order, k)
	}
	o.values[k] = v
}

func (o *object) remove(k string) {
	delete(o.values, k)
	for i, key := range o.order {
		if key == k {
			o.order = append(o.order[:i], o.order[i+1:]...)
			return
		}
	}
}

func (o *object) encode() json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.order {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.values[k])
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return buf.Bytes()
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// Names lists the blocked names, sorted, for reports.
func Names(blocked map[string]bool) []string {
	out := make([]string, 0, len(blocked))
	for n := range blocked {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
