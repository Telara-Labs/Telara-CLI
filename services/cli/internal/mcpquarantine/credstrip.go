package mcpquarantine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// Credential strips (TENG-3921). Block
// telara-documentation/architecture/estate-remediation.md ("Credential strips").
//
// A takedown of an OAuth client (a Google OAuth app, for example) never revokes
// the token at the provider. What the device does is take the credential out of
// the files it is printed in, keep it in an owner-only local record, and put it
// back exactly when the takedown is lifted. The tenant's list of clients to
// strip is the governance setting oauth_credential_striplist, served with the
// MCP blocklist. Telara never receives a credential value: the record stays on
// this device, and the scan reports only the location.
//
// Where: the same user-level MCP client files the MCP quarantine edits
// (discovery.QuarantinableMCPFiles), in each server's env, headers,
// http_headers and args. Nothing else is read today.
//
// Matching never guesses. A value is taken out when
//   - its SHA-256 equals a listed token_sha256 (the whole value, the part after
//     "=" in KEY=VALUE, or the part after "Bearer "), and, when that entry also
//     names a client id, the same server entry carries that client id; or
//   - the same server entry carries a listed client id (as a whole value, or as
//     the VALUE of KEY=VALUE) and the value sits under a secret-named key (env
//     or header name, KEY=VALUE argument, or the argument after a --token style
//     flag). The client id itself is never taken out.
// Empty values, ${VAR} or $VAR references, file paths and placeholders are
// never taken out.

// CredentialStriplistSettingKey is the tenant governance setting.
const CredentialStriplistSettingKey = "oauth_credential_striplist"

// CredentialStrip is one entry of the list: an OAuth client id, a token
// fingerprint (lowercase hex SHA-256), or both.
type CredentialStrip struct {
	ClientID    string `json:"client_id,omitempty"`
	TokenSHA256 string `json:"token_sha256,omitempty"`
}

func (s CredentialStrip) String() string {
	switch {
	case s.ClientID != "" && s.TokenSHA256 != "":
		return "client " + s.ClientID + " token sha256:" + s.TokenSHA256[:12]
	case s.TokenSHA256 != "":
		return "token sha256:" + s.TokenSHA256[:12]
	}
	return "client " + s.ClientID
}

var (
	stripClientID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	stripSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseCredentialStrips reads the setting value. A malformed value is an error,
// so the caller strips and restores nothing rather than guess.
func ParseCredentialStrips(value string) ([]CredentialStrip, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of objects: %w", CredentialStriplistSettingKey, err)
	}
	out := make([]CredentialStrip, 0, len(raw))
	for i, m := range raw {
		var s CredentialStrip
		for k, v := range m {
			var str string
			if err := json.Unmarshal(v, &str); err != nil {
				return nil, fmt.Errorf("%s entry %d field %q is not a string", CredentialStriplistSettingKey, i, k)
			}
			switch k {
			case "client_id":
				s.ClientID = str
			case "token_sha256":
				s.TokenSHA256 = str
			default:
				return nil, fmt.Errorf("%s entry %d has unknown field %q", CredentialStriplistSettingKey, i, k)
			}
		}
		if s.ClientID == "" && s.TokenSHA256 == "" {
			return nil, fmt.Errorf("%s entry %d names neither a client_id nor a token_sha256", CredentialStriplistSettingKey, i)
		}
		if s.ClientID != "" && !stripClientID.MatchString(s.ClientID) {
			return nil, fmt.Errorf("%s entry %d client_id is not an OAuth client id", CredentialStriplistSettingKey, i)
		}
		if s.TokenSHA256 != "" && !stripSHA256.MatchString(s.TokenSHA256) {
			return nil, fmt.Errorf("%s entry %d token_sha256 is not 64 lowercase hex characters", CredentialStriplistSettingKey, i)
		}
		out = append(out, s)
	}
	return out, nil
}

// CredentialRoot is where taken-out credentials are kept, owner-only.
func CredentialRoot(homeDir string) string {
	return filepath.Join(homeDir, ".telara", "quarantine", "credentials")
}

// CredentialRecord is one credential taken out of one place. Original is the
// exact string that was there; Placeholder is what was written instead.
type CredentialRecord struct {
	ID            string          `json:"id"`
	Client        string          `json:"client"` // MCP client, e.g. claude_code
	Path          string          `json:"path"`
	ItemsKey      string          `json:"items_key"`
	Server        string          `json:"server"` // the server entry's key
	Field         string          `json:"field"`  // env | headers | http_headers | args
	Name          string          `json:"name,omitempty"`
	Index         int             `json:"index"`
	Matched       CredentialStrip `json:"matched"`
	Original      string          `json:"original"`
	Placeholder   string          `json:"placeholder"`
	QuarantinedAt time.Time       `json:"quarantined_at"`
}

// Location names where the value sits, without the value.
func (r CredentialRecord) Location() string { return location{r.Field, r.Name, r.Index}.String() }

type location struct {
	Field string
	Name  string
	Index int
}

func (l location) String() string {
	if l.Field == "args" {
		return fmt.Sprintf("args[%d]", l.Index)
	}
	return l.Field + "." + l.Name
}

type candidate struct {
	loc   location
	value string
	// secretNamed is true when the value sits under a secret-named key.
	secretNamed bool
}

var credentialMapFields = []string{"env", "headers", "http_headers"}

// entryCandidates lists every string value of one server entry that could hold
// a credential, in a stable order.
func entryCandidates(entry *object) []candidate {
	var out []candidate
	for _, field := range credentialMapFields {
		raw, ok := entry.get(field)
		if !ok {
			continue
		}
		m, err := parseObject(raw)
		if err != nil {
			continue
		}
		for _, k := range m.keys() {
			v, _ := m.get(k)
			var s string
			if json.Unmarshal(v, &s) != nil {
				continue
			}
			out = append(out, candidate{loc: location{Field: field, Name: k, Index: -1}, value: s, secretNamed: secretWord.MatchString(k)})
		}
	}
	if raw, ok := entry.get("args"); ok {
		var args []json.RawMessage
		if json.Unmarshal(raw, &args) == nil {
			prev := ""
			for i, a := range args {
				var s string
				if json.Unmarshal(a, &s) != nil {
					prev = ""
					continue
				}
				named := false
				if k, _, found := strings.Cut(s, "="); found && secretWord.MatchString(k) {
					named = true
				} else if strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") && secretWord.MatchString(prev) {
					named = true
				}
				out = append(out, candidate{loc: location{Field: "args", Index: i}, value: s, secretNamed: named})
				prev = s
			}
		}
	}
	return out
}

// namesClient reports whether a value is the client id, alone or as the VALUE
// of KEY=VALUE.
func namesClient(v, clientID string) bool {
	if v == clientID {
		return true
	}
	_, rest, found := strings.Cut(v, "=")
	return found && rest == clientID
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// fingerprintMatches compares the value's possible token forms with fp.
func fingerprintMatches(v, fp string) bool {
	forms := []string{v}
	if _, rest, found := strings.Cut(v, "="); found {
		forms = append(forms, rest)
	}
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		forms = append(forms, strings.TrimSpace(v[7:]))
	}
	for _, f := range forms {
		if f != "" && sha256Hex(f) == fp {
			return true
		}
	}
	return false
}

// strippable is false for values that cannot be a printed credential.
func strippable(v string) bool {
	t := strings.TrimSpace(v)
	val := t
	if k, rest, found := strings.Cut(t, "="); found && k != "" && !strings.ContainsAny(k, " /") {
		val = rest
	}
	switch {
	case val == "", strings.Contains(t, discovery.QuarantinedCredentialPrefix):
		return false
	case strings.HasPrefix(val, "$"), strings.Contains(val, "${"):
		return false
	case strings.HasPrefix(val, "/"), strings.HasPrefix(val, "~/"), strings.HasPrefix(val, "./"), strings.HasPrefix(val, "../"),
		len(val) > 2 && val[1] == ':' && (val[2] == '\\' || val[2] == '/'):
		return false // a path to a credential file, not the credential
	}
	return true
}

// matchCandidates returns, for each candidate index to take out, the list entry
// that matched it.
func matchCandidates(cands []candidate, strips []CredentialStrip) map[int]CredentialStrip {
	present := map[string]bool{}
	for _, s := range strips {
		if s.ClientID == "" {
			continue
		}
		for _, c := range cands {
			if namesClient(c.value, s.ClientID) {
				present[s.ClientID] = true
				break
			}
		}
	}
	out := map[int]CredentialStrip{}
	for i, c := range cands {
		if !strippable(c.value) {
			continue
		}
		for _, s := range strips {
			if s.ClientID != "" && namesClient(c.value, s.ClientID) {
				continue // the client id is not a secret and stays in place
			}
			if s.TokenSHA256 != "" {
				if (s.ClientID == "" || present[s.ClientID]) && fingerprintMatches(c.value, s.TokenSHA256) {
					out[i] = s
					break
				}
				continue
			}
			if present[s.ClientID] && c.secretNamed {
				out[i] = s
				break
			}
		}
	}
	return out
}

// setCandidate writes s at loc in the entry.
func setCandidate(entry *object, loc location, s string) error {
	val, _ := json.Marshal(s)
	if loc.Field == "args" {
		raw, _ := entry.get("args")
		var args []json.RawMessage
		if err := json.Unmarshal(raw, &args); err != nil || loc.Index < 0 || loc.Index >= len(args) {
			return fmt.Errorf("args[%d] is not there", loc.Index)
		}
		args[loc.Index] = val
		body, err := json.Marshal(args)
		if err != nil {
			return err
		}
		entry.set("args", body)
		return nil
	}
	raw, ok := entry.get(loc.Field)
	if !ok {
		return fmt.Errorf("%s is not there", loc)
	}
	m, err := parseObject(raw)
	if err != nil {
		return fmt.Errorf("%s is not an object", loc.Field)
	}
	if _, ok := m.get(loc.Name); !ok {
		return fmt.Errorf("%s is not there", loc)
	}
	m.set(loc.Name, val)
	entry.set(loc.Field, m.compact())
	return nil
}

// placeholderFor keeps the KEY= of a KEY=VALUE argument so the argument stays
// recognisable, and marks the rest.
func placeholderFor(c candidate, id string) string {
	mark := discovery.QuarantinedCredentialPrefix + id
	if c.loc.Field == "args" {
		if k, _, found := strings.Cut(c.value, "="); found && secretWord.MatchString(k) {
			return k + "=" + mark
		}
	}
	return mark
}

// SyncCredentials takes listed credentials out of the user's MCP client files
// and puts back every one whose strip was lifted. It returns what it did even
// when it also returns an error. Call it only after the list was fetched and
// parsed: an unread list must never read as "nothing is stripped".
func SyncCredentials(homeDir string, strips []CredentialStrip, now time.Time) ([]Action, error) {
	var actions []Action
	var errs []error
	if len(strips) > 0 {
		for _, f := range discovery.QuarantinableMCPFiles(homeDir) {
			a, err := stripFile(homeDir, f, strips, now)
			actions = append(actions, a...)
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	a, err := restoreCredentials(homeDir, strips)
	actions = append(actions, a...)
	if err != nil {
		errs = append(errs, err)
	}
	return actions, errors.Join(errs...)
}

func stripFile(homeDir string, f discovery.MCPConfigFile, strips []CredentialStrip, now time.Time) ([]Action, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Path, err)
	}
	doc, err := parseObject(data)
	if err != nil {
		return nil, nil // not a JSON object: discovery reports it, nothing to strip
	}
	itemsRaw, ok := doc.get(f.ItemsKey)
	if !ok {
		return nil, nil
	}
	items, err := parseObject(itemsRaw)
	if err != nil {
		return nil, nil
	}
	var actions []Action
	changed := false
	for _, key := range items.keys() {
		raw, _ := items.get(key)
		entry, err := parseObject(raw)
		if err != nil {
			continue
		}
		cands := entryCandidates(entry)
		matches := matchCandidates(cands, strips)
		if len(matches) == 0 {
			continue
		}
		idx := make([]int, 0, len(matches))
		for i := range matches {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			c := cands[i]
			rec := CredentialRecord{Client: f.ClientID, Path: f.Path, ItemsKey: f.ItemsKey, Server: key, Field: c.loc.Field, Name: c.loc.Name,
				Index: c.loc.Index, Matched: matches[i], Original: c.value, QuarantinedAt: now.UTC()}
			rec.ID = credentialRecordID(rec)
			rec.Placeholder = placeholderFor(c, rec.ID)
			recPath, err := writeCredentialRecord(homeDir, rec)
			if err != nil {
				return actions, err
			}
			if err := setCandidate(entry, c.loc, rec.Placeholder); err != nil {
				return actions, fmt.Errorf("%s %s: %w", f.Path, key, err)
			}
			changed = true
			actions = append(actions, Action{Server: key, ClientID: f.ClientID, Path: f.Path, Kind: "credential_quarantined", Record: recPath,
				Note: rec.Location() + " (" + rec.Matched.String() + ")"})
		}
		items.set(key, entry.compact())
	}
	if !changed {
		return actions, nil
	}
	doc.set(f.ItemsKey, items.encode())
	if err := writeFileAtomic(f.Path, doc.encode()); err != nil {
		return actions, fmt.Errorf("rewrite %s (the credential records are kept and the next scan retries): %w", f.Path, err)
	}
	return actions, nil
}

func credentialRecordID(r CredentialRecord) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{r.Path, r.ItemsKey, r.Server, r.Location(), r.Original}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// writeCredentialRecord writes the record 0600 under a 0700 directory. A record
// already there for the same place and value (a rewrite that failed after the
// record was written) is reused.
func writeCredentialRecord(homeDir string, rec CredentialRecord) (string, error) {
	root := CredentialRoot(homeDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(root, rec.ID+".json")
	if prev, err := readCredentialRecord(path); err == nil {
		if prev.Path == rec.Path && prev.Server == rec.Server && prev.Location() == rec.Location() && prev.Original == rec.Original {
			return path, nil
		}
		return "", fmt.Errorf("a different credential record already exists at %s; the value was left in place", path)
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	return path, writeFileAtomic(path, body)
}

func readCredentialRecord(path string) (CredentialRecord, error) {
	var rec CredentialRecord
	raw, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(raw, &rec)
}

func restoreCredentials(homeDir string, strips []CredentialStrip) ([]Action, error) {
	entries, err := os.ReadDir(CredentialRoot(homeDir))
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
		recPath := filepath.Join(CredentialRoot(homeDir), e.Name())
		rec, err := readCredentialRecord(recPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("credential record %s: %w", recPath, err))
			continue
		}
		a, err := restoreOne(rec, recPath, strips)
		if a != nil {
			actions = append(actions, *a)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return actions, errors.Join(errs...)
}

func restoreOne(rec CredentialRecord, recPath string, strips []CredentialStrip) (*Action, error) {
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
	doc, err := parseObject(data)
	if err != nil {
		return conflict("the file is no longer a JSON object")
	}
	itemsRaw, ok := doc.get(rec.ItemsKey)
	if !ok {
		return conflict("the servers object is gone")
	}
	items, err := parseObject(itemsRaw)
	if err != nil {
		return conflict("the servers object is no longer an object")
	}
	raw, ok := items.get(rec.Server)
	if !ok {
		return conflict("the server entry is gone")
	}
	entry, err := parseObject(raw)
	if err != nil {
		return conflict("the server entry is no longer an object")
	}
	cands := entryCandidates(entry)
	at := -1
	for i, c := range cands {
		if c.loc.String() == rec.Location() {
			at = i
			break
		}
	}
	if at < 0 {
		return conflict("the value is gone")
	}
	// Still stripped? Judge the place as if the original were back.
	withOriginal := append([]candidate(nil), cands...)
	withOriginal[at].value = rec.Original
	if _, still := matchCandidates(withOriginal, strips)[at]; still {
		return nil, nil
	}
	switch cands[at].value {
	case rec.Original:
		// A rewrite failed after the record was written: nothing to put back.
	case rec.Placeholder:
		if err := setCandidate(entry, cands[at].loc, rec.Original); err != nil {
			return nil, fmt.Errorf("%s %s: %w", rec.Path, rec.Server, err)
		}
		items.set(rec.Server, entry.compact())
		doc.set(rec.ItemsKey, items.encode())
		if err := writeFileAtomic(rec.Path, doc.encode()); err != nil {
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

// compact encodes the object on one line, keeping key order; the file-level
// encode indents the whole document once.
func (o *object) compact() json.RawMessage {
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
	if err := json.Compact(&out, buf.Bytes()); err != nil {
		return buf.Bytes()
	}
	return out.Bytes()
}
