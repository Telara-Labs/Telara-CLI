package discovery

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// Managed MCP policy on this device (TENG-3921, enforced versus best effort).
// Block telara-documentation/architecture/estate-remediation.md ("Enforced or
// best effort").
//
// A remediation that takes an MCP server off a device is "enforced" only when
// the device's own managed policy denies that server, so a person cannot simply
// add it back. Claude Code reads that policy from managed-settings.json, which
// only an administrator can write. The scan reads it and marks each Claude Code
// server the policy denies.
//
// A server counts as denied when a deniedMcpServers entry matches its URL
// (wildcards allowed) or its exact command and arguments, or when
// allowManagedMcpServersOnly is on and no allowedMcpServers entry admits it.
// serverName is never treated as proof: the user chooses the name. An allow
// entry by name therefore admits the server (enforcement cannot be shown), and a
// deny entry by name does not count as a denial.

// managedSettingsPaths are Claude Code's managed settings locations.
// Package variable so tests can point at a temporary file.
var managedSettingsPaths = []string{
	"/Library/Application Support/ClaudeCode/managed-settings.json",
	"/etc/claude-code/managed-settings.json",
	`C:\ProgramData\ClaudeCode\managed-settings.json`,
}

type managedMCPEntry struct {
	ServerName    string   `json:"serverName"`
	ServerURL     string   `json:"serverUrl"`
	ServerCommand []string `json:"serverCommand"`
}

// ManagedMCPPolicy is the device's managed MCP policy, empty when none exists.
type ManagedMCPPolicy struct {
	present     bool
	denied      []managedMCPEntry
	allowed     []managedMCPEntry
	allowedSet  bool
	allowedOnly bool
}

// LoadManagedMCPPolicy reads the first managed settings file present. A file
// that cannot be parsed yields no policy, so nothing is claimed as enforced.
func LoadManagedMCPPolicy() ManagedMCPPolicy {
	for _, p := range managedSettingsPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			Denied      []managedMCPEntry  `json:"deniedMcpServers"`
			Allowed     *[]managedMCPEntry `json:"allowedMcpServers"`
			AllowedOnly bool               `json:"allowManagedMcpServersOnly"`
		}
		if json.Unmarshal(b, &doc) != nil {
			return ManagedMCPPolicy{}
		}
		pol := ManagedMCPPolicy{present: true, denied: doc.Denied, allowedOnly: doc.AllowedOnly}
		if doc.Allowed != nil {
			pol.allowedSet, pol.allowed = true, *doc.Allowed
		}
		return pol
	}
	return ManagedMCPPolicy{}
}

func managedURLMatch(pattern, raw string) bool {
	pattern, raw = strings.TrimSpace(pattern), strings.TrimSpace(raw)
	if pattern == "" || raw == "" {
		return false
	}
	re, err := regexp.Compile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$")
	return err == nil && re.MatchString(raw)
}

func managedCommandMatch(want []string, command string, args []string) bool {
	if len(want) == 0 || command == "" || len(want) != len(args)+1 || want[0] != command {
		return false
	}
	for i, a := range args {
		if want[i+1] != a {
			return false
		}
	}
	return true
}

func (e managedMCPEntry) matches(rawURL, command string, args []string) bool {
	return managedURLMatch(e.ServerURL, rawURL) || managedCommandMatch(e.ServerCommand, command, args)
}

// Present reports whether this device has a readable managed policy, which is
// what makes it a managed device for managed-devices-only (TENG-3921).
func (p ManagedMCPPolicy) Present() bool { return p.present }

// Denies reports whether this device's managed policy keeps the server out.
func (p ManagedMCPPolicy) Denies(name, rawURL, command string, args []string) bool {
	for _, e := range p.denied {
		if e.matches(rawURL, command, args) {
			return true
		}
	}
	if p.allowedSet && p.allowedOnly {
		for _, e := range p.allowed {
			if e.matches(rawURL, command, args) || (e.ServerName != "" && e.ServerName == name) {
				return false
			}
		}
		return true
	}
	return false
}
