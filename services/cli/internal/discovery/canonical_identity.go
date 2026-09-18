// Package mcpidentity is the shared canonical identity for discovered MCP
// servers (TENG-2508). CLI, SCM, and substrate observations must join on this
// algorithm, not on display name and not on either collector's older helper.
package discovery

import (
	"net/url"
	"path"
	"strings"
)

const (
	// StdioPrefix namespaces locally executed servers so they cannot collide
	// with a remote URL that happens to look like a package name.
	StdioPrefix = "stdio:"
	// ConnectorPrefix namespaces vendor-stable connector ids (Claude
	// connector id, etc.) so they do not collide with a URL or stdio key.
	ConnectorPrefix = "connector:"
)

var mcpTransportSuffixes = []string{"/sse", "/messages", "/message", "/stream"}

var packageRunners = map[string]bool{
	"npx": true, "pnpm": true, "yarn": true, "bunx": true,
	"uvx": true, "pipx": true, "uv": true,
}

var standaloneRunnerFlags = map[string]bool{
	"-y": true, "--yes": true, "-q": true, "--quiet": true,
	"--silent": true, "run": true, "tool": true, "exec": true, "-s": true,
}

// CanonicalRemoteIdentity returns a privacy-safe remote identity: scheme, host,
// port, and a normalized base path. Userinfo, query, fragment, trailing slash,
// and transport suffixes such as /sse are removed. A host alone is not
// enough: one host can serve several MCP servers on different paths.
//
// Raw URLs with userinfo, query, fragment, or credential-shaped values are
// rejected (empty string) so they never reach storage or an API response.
func CanonicalRemoteIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.User != nil {
		return ""
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	if looksCredentialShaped(raw) || looksCredentialShaped(parsed.Path) {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	out := scheme + "://" + host
	if port := parsed.Port(); port != "" {
		out += ":" + port
	}
	cleaned := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range mcpTransportSuffixes {
		if len(cleaned) >= len(suffix) && strings.EqualFold(cleaned[len(cleaned)-len(suffix):], suffix) {
			cleaned = strings.TrimRight(cleaned[:len(cleaned)-len(suffix)], "/")
			break
		}
	}
	if cleaned != "" && cleaned != "/" {
		out += cleaned
	}
	return out
}

// CanonicalStdioIdentity derives a source-independent key for a locally
// executed MCP server. Runners and flags are stripped; version pins are
// dropped; argument values that look like secrets never appear.
//
// npx -y @modelcontextprotocol/server-github and npx @modelcontextprotocol/server-github
// collapse to stdio:@modelcontextprotocol/server-github.
func CanonicalStdioIdentity(command string, args []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	base := strings.ToLower(path.Base(strings.ReplaceAll(command, "\\", "/")))
	if base == "" {
		return ""
	}
	if packageRunners[base] {
		if pkg := firstPackageArg(args); pkg != "" {
			return StdioPrefix + normalizePackage(pkg)
		}
		return ""
	}
	if base == "node" || base == "python" || base == "python3" || base == "deno" {
		if script := firstPackageArg(args); script != "" {
			if pkg := normalizePackage(path.Base(script)); pkg != "" {
				return StdioPrefix + pkg
			}
		}
	}
	return StdioPrefix + normalizePackage(base)
}

// CanonicalConnectorIdentity keys a vendor-stable connector id plus substrate.
func CanonicalConnectorIdentity(substrate, connectorID string) string {
	substrate = strings.ToLower(strings.TrimSpace(substrate))
	connectorID = strings.TrimSpace(connectorID)
	if substrate == "" || connectorID == "" {
		return ""
	}
	if looksCredentialShaped(connectorID) {
		return ""
	}
	return ConnectorPrefix + substrate + ":" + connectorID
}

// ObservationIdentity is the join key for one discovered MCP observation.
// First non-empty wins: canonical remote, then stdio, then vendor connector.
func ObservationIdentity(endpointIdentity, endpointHost, commandIdentity, substrate, connectorID string) string {
	if id := CanonicalRemoteIdentity(endpointIdentity); id != "" {
		return id
	}
	if id := CanonicalRemoteIdentity(endpointHost); id != "" {
		return id
	}
	if commandIdentity != "" {
		if strings.HasPrefix(commandIdentity, StdioPrefix) {
			return commandIdentity
		}
		// Legacy CLI wire emitted "npx:-y:@pkg". Parse that before treating
		// the whole string as a binary name.
		parts := strings.Split(commandIdentity, ":")
		if len(parts) > 1 && packageRunners[strings.ToLower(parts[0])] {
			if id := CanonicalStdioIdentity(parts[0], parts[1:]); id != "" {
				return id
			}
		}
		if id := CanonicalStdioIdentity(commandIdentity, nil); id != "" {
			return id
		}
	}
	return CanonicalConnectorIdentity(substrate, connectorID)
}

// RegistrationIdentity is the join key for a custom MCP registration.
func RegistrationIdentity(serverURL, command string, commandArgs []string) string {
	if id := CanonicalRemoteIdentity(serverURL); id != "" {
		return id
	}
	return CanonicalStdioIdentity(command, commandArgs)
}

// IsTelaraManaged reports whether the identity is Telara's own MCP endpoint
// (DISCOVERED_MANAGED, never a candidate).
func IsTelaraManaged(identity string, managedHosts []string) bool {
	id := strings.ToLower(strings.TrimSpace(identity))
	if id == "" {
		return false
	}
	parsed, err := url.Parse(id)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "api.telara.dev" || strings.HasSuffix(host, ".telara.dev") {
		return true
	}
	for _, h := range managedHosts {
		n := CanonicalRemoteIdentity(h)
		if n == "" {
			continue
		}
		if identityHostsEqual(id, n) {
			return true
		}
	}
	return false
}

func identityHostsEqual(a, b string) bool {
	pa, errA := url.Parse(a)
	pb, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(pa.Scheme, pb.Scheme) &&
		strings.EqualFold(pa.Hostname(), pb.Hostname()) &&
		pa.Port() == pb.Port()
}

func firstPackageArg(args []string) string {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		if standaloneRunnerFlags[strings.ToLower(trimmed)] {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			continue
		}
		if looksCredentialShaped(trimmed) {
			continue
		}
		return trimmed
	}
	return ""
}

func normalizePackage(pkg string) string {
	pkg = strings.ToLower(strings.TrimSpace(pkg))
	if pkg == "" {
		return ""
	}
	scoped := strings.HasPrefix(pkg, "@")
	body := pkg
	if scoped {
		body = pkg[1:]
	}
	if at := strings.LastIndex(body, "@"); at > 0 {
		body = body[:at]
	}
	if scoped {
		return "@" + body
	}
	return body
}

func looksCredentialShaped(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"bearer ", "basic ", "token=", "secret=", "password=",
		"apikey=", "api_key=", "access_token=", "private_key",
		"-----begin ", "?token", "&token", "?key=", "&key=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
