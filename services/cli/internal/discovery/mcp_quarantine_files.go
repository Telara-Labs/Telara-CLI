package discovery

import (
	"path/filepath"
	"strings"
)

// MCPConfigFile is one user-level MCP configuration file whose servers are an
// object keyed by server name, the shape a quarantine can remove an entry from
// and put it back (TENG-3921).
type MCPConfigFile struct {
	ClientID string
	Path     string
	// ItemsKey is the top-level key holding the servers object.
	ItemsKey string
}

// QuarantinableMCPFiles derives the files from the discovery registry, so
// discovery and quarantine never disagree about where servers live. Only
// global-scope JSON sources whose servers are a top-level object qualify.
// Project files belong to a repository, and managed files belong to the
// administrator's device policy, so neither is edited.
func QuarantinableMCPFiles(homeDir string) []MCPConfigFile {
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
				if !strings.HasPrefix(p, "~/") || !strings.HasSuffix(p, ".json") {
					continue
				}
				out = append(out, MCPConfigFile{ClientID: src.ClientID, Path: filepath.Join(homeDir, filepath.FromSlash(p[2:])), ItemsKey: res.ItemsPath})
			}
		}
	}
	return out
}
