package mcpquarantine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2/unstable"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// Credential strips in files edited in place (TENG-3921): TOML MCP client
// files (Codex ~/.codex/config.toml) and gcloud application default
// credentials. Only the bytes of the value taken out change; every other byte
// of the file, comments and layout included, stays as it was. A record keeps
// the value's exact source bytes, so a restore is byte-identical.

const (
	formatTOML = "toml"
	formatADC  = "adc"
)

type spanTarget struct {
	Format   string
	Client   string
	Path     string
	ItemsKey string
}

func spanTargets(homeDir string) []spanTarget {
	var out []spanTarget
	for _, f := range discovery.CredentialTOMLFiles(homeDir) {
		out = append(out, spanTarget{Format: formatTOML, Client: f.ClientID, Path: f.Path, ItemsKey: f.ItemsKey})
	}
	for _, p := range discovery.GoogleADCFiles(homeDir) {
		out = append(out, spanTarget{Format: formatADC, Client: discovery.GoogleADCClient, Path: p})
	}
	return out
}

// spanCandidate is a candidate with the byte range of its value in the file.
type spanCandidate struct {
	candidate
	start, end int
}

// spanEntries are a file's candidates per server entry ("" for gcloud).
type spanEntries struct {
	order   []string
	entries map[string][]spanCandidate
}

func (e *spanEntries) add(server string, c spanCandidate) {
	if e.entries == nil {
		e.entries = map[string][]spanCandidate{}
	}
	if _, ok := e.entries[server]; !ok {
		e.order = append(e.order, server)
	}
	e.entries[server] = append(e.entries[server], c)
}

func (e *spanEntries) plain(server string) []candidate {
	out := make([]candidate, 0, len(e.entries[server]))
	for _, c := range e.entries[server] {
		out = append(out, c.candidate)
	}
	return out
}

func parseSpans(format string, data []byte, itemsKey string) (*spanEntries, error) {
	switch format {
	case formatTOML:
		return tomlSpans(data, itemsKey)
	case formatADC:
		return adcSpans(data)
	}
	return nil, fmt.Errorf("unknown credential file format %q", format)
}

// tomlSpans walks every key/value of the document and keeps the string values
// under <itemsKey>.<server>.{env,headers,http_headers}.<name> and
// <itemsKey>.<server>.args[i], with their byte ranges. Dotted keys, inline
// tables and [table] headers all resolve to the same full key path.
func tomlSpans(data []byte, itemsKey string) (*spanEntries, error) {
	out := &spanEntries{}
	var p unstable.Parser
	p.Reset(data)
	var table []string
	inArrayTable := false
	keyOf := func(it unstable.Iterator) []string {
		var k []string
		for it.Next() {
			k = append(k, string(it.Node().Data))
		}
		return k
	}
	args := map[string][]spanCandidate{}
	var argOrder []string
	emit := func(path []string, index int, n *unstable.Node) {
		if len(path) < 3 || path[0] != itemsKey {
			return
		}
		server, rest := path[1], path[2:]
		start := int(n.Raw.Offset)
		c := spanCandidate{start: start, end: start + int(n.Raw.Length)}
		c.value = string(n.Data)
		switch {
		case index >= 0 && len(rest) == 1 && rest[0] == "args":
			c.loc = location{Field: "args", Index: index}
			if _, ok := args[server]; !ok {
				argOrder = append(argOrder, server)
			}
			args[server] = append(args[server], c)
		case index < 0 && len(rest) == 2 && (rest[0] == "env" || rest[0] == "headers" || rest[0] == "http_headers"):
			c.loc = location{Field: rest[0], Name: rest[1], Index: -1}
			c.secretNamed = secretWord.MatchString(rest[1])
			out.add(server, c)
		}
	}
	var walk func(path []string, v *unstable.Node)
	walk = func(path []string, v *unstable.Node) {
		switch v.Kind {
		case unstable.String:
			emit(path, -1, v)
		case unstable.Array:
			it := v.Children()
			i := 0
			for it.Next() {
				if n := it.Node(); n.Kind == unstable.String {
					emit(path, i, n)
				}
				i++
			}
		case unstable.InlineTable:
			it := v.Children()
			for it.Next() {
				kv := it.Node()
				walk(append(append([]string(nil), path...), keyOf(kv.Key())...), kv.Value())
			}
		}
	}
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.Table:
			table, inArrayTable = keyOf(e.Key()), false
		case unstable.ArrayTable:
			table, inArrayTable = keyOf(e.Key()), true
		case unstable.KeyValue:
			if inArrayTable {
				continue // an array of tables is not a server entry
			}
			walk(append(append([]string(nil), table...), keyOf(e.Key())...), e.Value())
		}
	}
	if err := p.Error(); err != nil {
		return nil, err
	}
	// Arguments: a value is secret-named by its own KEY= or by the flag before it.
	for _, server := range argOrder {
		prev := ""
		for _, c := range args[server] {
			if k, _, found := strings.Cut(c.value, "="); found && secretWord.MatchString(k) {
				c.secretNamed = true
			} else if strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") && secretWord.MatchString(prev) {
				c.secretNamed = true
			}
			out.add(server, c)
			prev = c.value
		}
	}
	return out, nil
}

// adcSpans reads a gcloud application default credentials file. Only an
// authorized_user file holds a user's OAuth credential: its client_id says
// which client, and client_secret and refresh_token are the secrets. Any other
// type (a service account key, an external account) yields nothing.
func adcSpans(data []byte) (*spanEntries, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	type field struct {
		value      string
		start, end int
	}
	fields := map[string]field{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		end := int(dec.InputOffset())
		var s string
		if json.Unmarshal(raw, &s) == nil {
			fields[key] = field{value: s, start: end - len(raw), end: end}
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after the object")
	}
	out := &spanEntries{}
	if fields["type"].value != "authorized_user" {
		return out, nil
	}
	for _, k := range []string{"client_id", "client_secret", "refresh_token"} {
		f, ok := fields[k]
		if !ok {
			continue
		}
		c := spanCandidate{start: f.start, end: f.end}
		c.loc, c.value, c.secretNamed = location{Field: "json", Name: k, Index: -1}, f.value, k != "client_id"
		out.add("", c)
	}
	return out, nil
}

// encodeSpanValue is how a placeholder is written in the file's own syntax.
func encodeSpanValue(format, s string) []byte {
	if format == formatTOML {
		var b strings.Builder
		b.WriteByte('"')
		for _, r := range s {
			switch {
			case r == '"' || r == '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, "\\u%04X", r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
		return []byte(b.String())
	}
	out, _ := json.Marshal(s)
	return out
}

type spanEdit struct {
	start, end int
	with       []byte
}

func applySpanEdits(data []byte, edits []spanEdit) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := append([]byte(nil), data...)
	for _, e := range edits {
		out = append(out[:e.start], append(append([]byte(nil), e.with...), out[e.end:]...)...)
	}
	return out
}

func stripSpanFile(homeDir string, t spanTarget, strips []CredentialStrip, now time.Time) ([]Action, error) {
	data, err := os.ReadFile(t.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", t.Path, err)
	}
	doc, err := parseSpans(t.Format, data, t.ItemsKey)
	if err != nil {
		return nil, nil // unparseable: nothing can be placed safely, nothing is changed
	}
	var actions []Action
	var edits []spanEdit
	for _, server := range doc.order {
		spans := doc.entries[server]
		matches := matchCandidates(doc.plain(server), strips)
		idx := make([]int, 0, len(matches))
		for i := range matches {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			c := spans[i]
			rec := CredentialRecord{Client: t.Client, Path: t.Path, ItemsKey: t.ItemsKey, Server: server, Field: c.loc.Field, Name: c.loc.Name,
				Index: c.loc.Index, Matched: matches[i], Original: c.value, QuarantinedAt: now.UTC(), Format: t.Format, OriginalRaw: string(data[c.start:c.end])}
			rec.ID = credentialRecordID(rec)
			rec.Placeholder = placeholderFor(c.candidate, rec.ID)
			recPath, err := writeCredentialRecord(homeDir, rec)
			if err != nil {
				return actions, err
			}
			edits = append(edits, spanEdit{start: c.start, end: c.end, with: encodeSpanValue(t.Format, rec.Placeholder)})
			actions = append(actions, Action{Server: server, ClientID: t.Client, Path: t.Path, Kind: "credential_quarantined", Record: recPath,
				Note: rec.Location() + " (" + rec.Matched.String() + ")"})
		}
	}
	if len(edits) == 0 {
		return actions, nil
	}
	if err := writeFileAtomic(t.Path, applySpanEdits(data, edits)); err != nil {
		return actions, fmt.Errorf("rewrite %s (the credential records are kept and the next scan retries): %w", t.Path, err)
	}
	return actions, nil
}

func restoreSpan(rec CredentialRecord, recPath string, strips []CredentialStrip) (*Action, error) {
	act := &Action{Server: rec.Server, ClientID: rec.Client, Path: rec.Path, Record: recPath}
	conflict := func(note string) (*Action, error) {
		act.Kind, act.Note = "credential_conflict", rec.Location()+": "+note+"; the credential stays in "+recPath
		return act, nil
	}
	data, err := os.ReadFile(rec.Path)
	if errors.Is(err, os.ErrNotExist) {
		return conflict("the file is gone")
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rec.Path, err)
	}
	doc, err := parseSpans(rec.Format, data, rec.ItemsKey)
	if err != nil {
		return conflict("the file no longer parses")
	}
	spans, ok := doc.entries[rec.Server]
	if !ok {
		return conflict("the server entry is gone")
	}
	at := -1
	for i, c := range spans {
		if c.loc.String() == rec.Location() {
			at = i
			break
		}
	}
	if at < 0 {
		return conflict("the value is gone")
	}
	withOriginal := doc.plain(rec.Server)
	withOriginal[at].value = rec.Original
	if _, still := matchCandidates(withOriginal, strips)[at]; still {
		return nil, nil
	}
	switch spans[at].value {
	case rec.Original:
		// A rewrite failed after the record was written: nothing to put back.
	case rec.Placeholder:
		edited := applySpanEdits(data, []spanEdit{{start: spans[at].start, end: spans[at].end, with: []byte(rec.OriginalRaw)}})
		if err := writeFileAtomic(rec.Path, edited); err != nil {
			return nil, fmt.Errorf("rewrite %s: %w", rec.Path, err)
		}
	default:
		return conflict("the value was changed by hand")
	}
	if err := os.Remove(recPath); err != nil {
		return nil, fmt.Errorf("restored %s in %s but could not remove %s: %w", rec.Location(), rec.Path, recPath, err)
	}
	act.Kind, act.Note = "credential_restored", rec.Location()
	return act, nil
}
