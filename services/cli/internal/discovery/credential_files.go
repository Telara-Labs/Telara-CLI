package discovery

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Credential files a credential strip reads (TENG-3921). Block
// telara-documentation/architecture/estate-remediation.md ("Credential strips").

// CredentialTOMLFiles are the user-level TOML MCP files whose servers are a
// top-level table (Codex ~/.codex/config.toml, mcp_servers), derived from the
// same registry as QuarantinableMCPFiles. Project and managed files are never
// listed.
func CredentialTOMLFiles(homeDir string) []MCPConfigFile {
	sources, _ := fileSources()
	var out []MCPConfigFile
	for _, src := range sources {
		if src.Scope != ScopeGlobal {
			continue
		}
		for _, res := range src.Resources {
			if res.Kind != "mcp_server_deployment" || res.IDField != "_key" || res.ItemsPath == "" || strings.Contains(res.ItemsPath, ".") {
				continue
			}
			for _, p := range src.ConfigPaths {
				if !strings.HasPrefix(p, "~/") || !strings.HasSuffix(p, ".toml") {
					continue
				}
				out = append(out, MCPConfigFile{ClientID: src.ClientID, Path: filepath.Join(homeDir, filepath.FromSlash(p[2:])), ItemsKey: res.ItemsPath})
			}
		}
	}
	return out
}

// GoogleADCClient names gcloud's application default credentials in reports.
const GoogleADCClient = "gcloud"

// gcloudConfigDir is where gcloud keeps its configuration: CLOUDSDK_CONFIG when
// set (gcloud's own override), %APPDATA%\gcloud on Windows, else
// ~/.config/gcloud.
func gcloudConfigDir(homeDir string) string {
	if dir := strings.TrimSpace(os.Getenv("CLOUDSDK_CONFIG")); dir != "" {
		return dir
	}
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "gcloud")
		}
	}
	return filepath.Join(homeDir, ".config", "gcloud")
}

// GoogleADCFiles are gcloud's application default credentials files: the
// current one and each legacy per-account copy. An authorized_user file holds a
// client id, client secret and refresh token in plain text.
func GoogleADCFiles(homeDir string) []string {
	dir := gcloudConfigDir(homeDir)
	out := []string{filepath.Join(dir, "application_default_credentials.json")}
	legacy, _ := filepath.Glob(filepath.Join(dir, "legacy_credentials", "*", "adc.json"))
	sort.Strings(legacy)
	return append(out, legacy...)
}

// CredentialYAMLFiles are the user-level YAML MCP files, derived from the same
// registry as QuarantinableMCPFiles: Continue ~/.continue/config.yaml
// (mcpServers, a list named by "name") and Goose ~/.config/goose/config.yaml
// (extensions, keyed by name). Goose's own config directory also moves with
// XDG_CONFIG_HOME and, on Windows, lives under %APPDATA%\Block\goose\config;
// both are listed beside the registry path. Project and managed files are
// never listed.
func CredentialYAMLFiles(homeDir string) []MCPConfigFile {
	sources, _ := fileSources()
	var out []MCPConfigFile
	seen := map[string]bool{}
	add := func(f MCPConfigFile) {
		if !seen[f.Path] {
			seen[f.Path] = true
			out = append(out, f)
		}
	}
	for _, src := range sources {
		if src.Scope != ScopeGlobal {
			continue
		}
		for _, res := range src.Resources {
			if res.Kind != "mcp_server_deployment" || res.ItemsPath == "" || res.IDField == "" || strings.Contains(res.ItemsPath, ".") {
				continue
			}
			for _, p := range src.ConfigPaths {
				lower := strings.ToLower(p)
				if !strings.HasPrefix(p, "~/") || !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) {
					continue
				}
				f := MCPConfigFile{ClientID: src.ClientID, Path: filepath.Join(homeDir, filepath.FromSlash(p[2:])), ItemsKey: res.ItemsPath, IDField: res.IDField}
				add(f)
				if src.ClientID == ClientGoose {
					for _, dir := range gooseConfigDirs() {
						f.Path = filepath.Join(dir, "config.yaml")
						add(f)
					}
				}
			}
		}
	}
	return out
}

// gooseConfigDirs are Goose's config directories other than ~/.config/goose.
func gooseConfigDirs() []string {
	var out []string
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" && runtime.GOOS != "windows" {
		out = append(out, filepath.Join(x, "goose"))
	}
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			out = append(out, filepath.Join(appData, "Block", "goose", "config"))
		}
	}
	return out
}

// UnreadCredentialFiles are files known to hold MCP credentials in a shape no
// credential strip reads: Goose's secrets.yaml (the values of an extension's
// env_keys when the OS keyring is off), a flat mapping with no server entry to
// match a client id against. A strip reports each one that exists as
// credential_unhandled, and the bindings scope is incomplete while one exists,
// so nothing reads it as checked.
func UnreadCredentialFiles(homeDir string) []MCPConfigFile {
	dirs := append([]string{filepath.Join(homeDir, ".config", "goose")}, gooseConfigDirs()...)
	out := make([]MCPConfigFile, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, MCPConfigFile{ClientID: ClientGoose, Path: filepath.Join(d, "secrets.yaml")})
	}
	return out
}
