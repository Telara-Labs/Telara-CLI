package mcpquarantine

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

// Credential bindings for the scan report (TENG-3921). Block
// telara-documentation/architecture/estate-remediation.md ("Credential strips").
//
// For every place a credential strip reads, CredentialBindings reports each
// OAuth client whose credential sits there: present (printed in plain text) or
// quarantined (a strip took it out). A client is recognised when its id is on
// the tenant's strip list, when it has the shape of a Google OAuth client id,
// or when it is the client_id of a gcloud authorized_user file. Present means a
// client-wide strip of that client would take something out, by the same rules
// a strip uses. Unhandled means it would, but at least one of those values
// cannot be edited byte for byte, so a strip refuses and the credential stays:
// it never counts as done. No value is reported, only key names.

var googleClientIDShape = regexp.MustCompile(`^[0-9]+-[a-z0-9]+\.apps\.googleusercontent\.com$`)

// clientIDsIn lists the client ids an entry carries.
func clientIDsIn(cands []candidate, strips []CredentialStrip, adc bool) []string {
	seen := map[string]bool{}
	for _, c := range cands {
		v := c.value
		if _, rest, found := strings.Cut(v, "="); found {
			if googleClientIDShape.MatchString(rest) {
				seen[rest] = true
			}
		}
		if googleClientIDShape.MatchString(v) || (adc && c.loc.Name == "client_id" && stripClientID.MatchString(v)) {
			seen[v] = true
		}
		for _, s := range strips {
			if s.ClientID != "" && namesClient(v, s.ClientID) {
				seen[s.ClientID] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func entryBindings(client, server string, cands []candidate, strips []CredentialStrip, adc bool) []discovery.CredentialBinding {
	var out []discovery.CredentialBinding
	for _, id := range clientIDsIn(cands, strips, adc) {
		b := discovery.CredentialBinding{Client: client, Server: server, OAuthClientID: id}
		unhandled := false
		for i := range matchCandidates(cands, []CredentialStrip{{ClientID: id}}) {
			b.Locations = append(b.Locations, cands[i].loc.String())
			unhandled = unhandled || cands[i].unhandled != ""
		}
		if len(b.Locations) > 0 {
			b.State = discovery.CredentialStatePresent
			if unhandled {
				b.State = discovery.CredentialStateUnhandled
			}
		} else {
			for _, c := range cands {
				if strings.Contains(c.value, discovery.QuarantinedCredentialPrefix) {
					b.Locations = append(b.Locations, c.loc.String())
				}
			}
			if len(b.Locations) == 0 {
				continue // the client id alone, with no credential beside it
			}
			b.State = discovery.CredentialStateQuarantined
		}
		sort.Strings(b.Locations)
		out = append(out, b)
	}
	return out
}

// CredentialBindings reads every credential file a strip reads. complete is
// false when any of them could not be read or parsed, so the report never
// withdraws a binding it could not see.
func CredentialBindings(homeDir string, strips []CredentialStrip) ([]discovery.CredentialBinding, bool) {
	var out []discovery.CredentialBinding
	complete := true
	read := func(path string) ([]byte, bool) {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false
		}
		if err != nil {
			complete = false
			return nil, false
		}
		return data, true
	}
	for _, f := range discovery.QuarantinableMCPFiles(homeDir) {
		data, ok := read(f.Path)
		if !ok {
			continue
		}
		doc, err := parseObject(data)
		if err != nil {
			complete = false
			continue
		}
		raw, ok := doc.get(f.ItemsKey)
		if !ok || strings.TrimSpace(string(raw)) == "null" {
			continue
		}
		items, err := parseObject(raw)
		if err != nil {
			complete = false // servers that are not an object were not read
			continue
		}
		for _, key := range items.keys() {
			v, _ := items.get(key)
			entry, err := parseObject(v)
			if err != nil {
				complete = false
				continue
			}
			out = append(out, entryBindings(f.ClientID, key, entryCandidates(entry), strips, false)...)
		}
	}
	for _, t := range spanTargets(homeDir) {
		data, ok := read(t.Path)
		if !ok {
			continue
		}
		doc, err := parseSpans(t, data)
		if err != nil {
			complete = false
			continue
		}
		for _, server := range doc.order {
			out = append(out, entryBindings(t.Client, server, doc.plain(server), strips, t.Format == formatADC)...)
		}
	}
	// A file known to hold credentials in a shape no strip reads keeps the
	// scope incomplete while it exists.
	for _, f := range discovery.UnreadCredentialFiles(homeDir) {
		if _, ok := read(f.Path); ok {
			complete = false
		}
	}
	return out, complete
}
