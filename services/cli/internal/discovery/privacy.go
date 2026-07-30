package discovery

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PathClass reduces a config path to a non-identifying category.
func PathClass(path string) string {
	cleaned := filepath.Clean(path)
	normalized := filepath.ToSlash(cleaned)

	if isManagedPath(normalized) {
		return "managed_system"
	}

	// A path under the home directory is only USER-GLOBAL when it is one of the
	// known global config locations exactly. Developers keep their projects
	// under home too, so treating "anything under home" as user_global
	// misclassified essentially every project-scoped config. Scope drives
	// tombstone authority, so a wrong scope label is not cosmetic.
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		rel, relErr := filepath.Rel(home, cleaned)
		if relErr == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			if class := userPathClass(filepath.ToSlash(rel)); class != "" {
				return class
			}
		}
	}

	if isProjectPath(normalized) {
		return "project_local"
	}

	if filepath.IsAbs(cleaned) {
		return "managed_system"
	}

	return "project_local"
}

// NormalizeEndpointHost returns only the scheme, host, and port for a remote endpoint.
func NormalizeEndpointHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "unknown"
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.Scheme + "://" + parsed.Host
}

// ClassifyCredential returns a credential category and key-name-only hint.
func ClassifyCredential(headers map[string]string, env map[string]string) (CredentialClass, string) {
	if key := firstMatchingKey(headers, func(k, v string) bool {
		return isCredentialKey(k) && isInlineSecret(v)
	}); key != "" {
		return CredentialInline, key
	}
	if key := firstMatchingKey(env, func(k, v string) bool {
		return isCredentialKey(k) && isInlineSecret(v)
	}); key != "" {
		return CredentialInline, key
	}
	if key := firstMatchingKey(headers, func(k, v string) bool {
		return isCredentialKey(k) && isEnvReference(v)
	}); key != "" {
		return CredentialEnvReferenced, key
	}
	if key := firstMatchingKey(env, func(k, v string) bool {
		return isCredentialKey(k) && (v == "" || isEnvReference(v))
	}); key != "" {
		return CredentialEnvReferenced, key
	}
	if key := firstMatchingKey(headers, func(k, v string) bool {
		return strings.Contains(strings.ToLower(k), "oauth") || strings.Contains(strings.ToLower(v), "oauth")
	}); key != "" {
		return CredentialOAuthManaged, key
	}
	if key := firstMatchingKey(env, func(k, v string) bool {
		return strings.Contains(strings.ToLower(k), "oauth") || strings.Contains(strings.ToLower(v), "oauth")
	}); key != "" {
		return CredentialOAuthManaged, key
	}
	if len(headers) == 0 && len(env) == 0 {
		return CredentialNone, ""
	}
	if key := firstCredentialLikeKey(headers); key != "" {
		return CredentialUnknown, key
	}
	if key := firstCredentialLikeKey(env); key != "" {
		return CredentialUnknown, key
	}
	return CredentialNone, ""
}

// NormalizeCommandIdentity strips filesystem paths and keeps only safe identity signals.
func NormalizeCommandIdentity(command string, args []string) string {
	base := commandBase(command)
	if base == "" {
		return ""
	}
	if !isPackageRunner(base) {
		return base
	}

	parts := []string{base}
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" || looksSensitive(arg) {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if strings.Contains(arg, "=") {
				continue
			}
			if isRunnerFlagWithValue(arg) && i+1 < len(args) {
				i++
				continue
			}
			if isSafeRunnerFlag(arg) {
				parts = append(parts, arg)
			}
			continue
		}
		if isSafePackageIdentity(arg) {
			parts = append(parts, packageIdentity(arg))
			break
		}
	}
	return strings.Join(parts, ":")
}

func isManagedPath(path string) bool {
	return strings.HasPrefix(path, "/Library/") ||
		strings.HasPrefix(path, "/etc/") ||
		strings.HasPrefix(path, "/ProgramData/") ||
		strings.Contains(path, ":/ProgramData/")
}

func userPathClass(rel string) string {
	switch {
	case rel == ".claude.json", rel == ".claude/settings.json":
		return "user_global:claude_code"
	case rel == ".cursor/mcp.json":
		return "user_global:cursor"
	case rel == ".codex/config.toml":
		return "user_global:codex"
	case rel == ".gemini/settings.json":
		return "user_global:gemini"
	case rel == ".codeium/windsurf/mcp_config.json":
		return "user_global:windsurf"
	case rel == ".aws/amazonq/mcp.json":
		return "user_global:amazon_q"
	default:
		// Not a recognised global location. Empty means "undecided" so the
		// caller can fall through to the project check rather than asserting
		// a user-global scope we have not established.
		return ""
	}
}

func isProjectPath(path string) bool {
	projectSuffixes := []string{
		"/.mcp.json",
		"/.claude/settings.json",
		"/.cursor/mcp.json",
		"/.codex/config.toml",
		"/.gemini/settings.json",
		"/.vscode/mcp.json",
		"/.windsurf/mcp_config.json",
		"/.amazonq/mcp.json",
	}
	for _, suffix := range projectSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func firstMatchingKey(values map[string]string, match func(string, string) bool) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if match(key, values[key]) {
			return key
		}
	}
	return ""
}

func firstCredentialLikeKey(values map[string]string) string {
	return firstMatchingKey(values, func(k, _ string) bool { return isCredentialKey(k) })
}

func isCredentialKey(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	credentialTerms := []string{
		"authorization",
		"auth",
		"api_key",
		"apikey",
		"access_token",
		"refresh_token",
		"token",
		"secret",
		"password",
		"credential",
	}
	for _, term := range credentialTerms {
		if strings.Contains(k, term) {
			return true
		}
	}
	return false
}

func isInlineSecret(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" || isEnvReference(v) {
		return false
	}
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "basic ") {
		return true
	}
	return len(v) >= 12 && secretAlphabet.MatchString(v)
}

func isEnvReference(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	return envBraceReference.MatchString(v) || envDollarReference.MatchString(v)
}

func looksSensitive(value string) bool {
	lower := strings.ToLower(value)
	return isInlineSecret(value) ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "password") ||
		strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "api_key")
}

func commandBase(command string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(command, "\\", "/"))
	if cleaned == "" {
		return ""
	}
	return filepath.Base(cleaned)
}

func isPackageRunner(base string) bool {
	switch strings.ToLower(base) {
	case "npx", "pnpx", "uvx", "pipx", "bunx":
		return true
	default:
		return false
	}
}

func isRunnerFlagWithValue(flag string) bool {
	switch flag {
	case "--package", "--from", "--registry", "--cache", "--cwd", "--prefix", "-p", "-c":
		return true
	default:
		return false
	}
}

func isSafeRunnerFlag(flag string) bool {
	switch flag {
	case "-y", "--yes":
		return true
	default:
		return false
	}
}

func isSafePackageIdentity(arg string) bool {
	if strings.Contains(arg, "://") || strings.Contains(arg, "@@") {
		return false
	}
	return !strings.ContainsAny(arg, " \t\n\r")
}

func packageIdentity(arg string) string {
	return strings.Trim(strings.ReplaceAll(arg, "\\", "/"), "/")
}

var (
	envBraceReference  = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
	envDollarReference = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*$`)
	secretAlphabet     = regexp.MustCompile(`[A-Za-z].*[0-9]|[0-9].*[A-Za-z]`)
)
