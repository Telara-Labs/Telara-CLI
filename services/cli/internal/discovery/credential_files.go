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
