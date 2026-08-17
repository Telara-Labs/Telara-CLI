package discovery

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	catalog "gitlab.com/telara-labs/telara-utilities/go/integrations/catalog"
)

// catalog_engine_test.go exercises the catalog-driven §7 engine loop
// (scanner.go + catalog.go) against the REAL embedded telara-utilities
// catalog wherever possible — this is the "real catalog loader plus real
// file fixtures, not Go struct literals" the task requires. Only
// TestUnresolvableIdentityDroppedAndCounted needs a synthetic catalog
// (production id_field values are always non-empty by construction), and
// even there the catalog is real YAML text run through the real loader — a
// fstest.MapFS standing in for a definitions/ directory, not a
// catalogFileSource{} struct literal.

// TestParityWithHardcodedScanner proves the catalog-driven scanner detects
// exactly what the old hardcoded scanSpec table detected, for all 7 clients
// it used to cover: claude-code, cursor, windsurf, vscode, codex, gemini,
// amazon-q. Six of these are now real catalog rows (telara-utilities commits
// 843129c4 / 89a1a847); vscode is the one gap — see
// provisionalFallbackSources in catalog.go for why, and this test proves
// that fallback keeps working too. A negative case proves an unscanned
// location does not leak into a result.
func TestParityWithHardcodedScanner(t *testing.T) {
	type tc struct {
		name       string
		client     string
		scope      string
		configPath func(home, cwd string) string
		fixture    string
		wantServer string
		wantHost   string
		wantCmd    string
	}

	cases := []tc{
		{
			name:   "claude-code global",
			client: ClientClaudeCode,
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".claude.json")
			},
			fixture: `{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`,
			wantServer: "github", wantCmd: "npx:-y:@modelcontextprotocol/server-github",
		},
		{
			name:   "claude-code project",
			client: ClientClaudeCode,
			scope:  ScopeProject,
			configPath: func(home, cwd string) string {
				return filepath.Join(cwd, ".mcp.json")
			},
			fixture:    `{"mcpServers":{"internal-tool":{"url":"https://mcp.acme.example/sse"}}}`,
			wantServer: "internal-tool", wantHost: "https://mcp.acme.example",
		},
		{
			name:   "cursor global",
			client: ClientCursor,
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".cursor", "mcp.json")
			},
			fixture:    `{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`,
			wantServer: "github", wantCmd: "npx:-y:@modelcontextprotocol/server-github",
		},
		{
			name:   "windsurf global",
			client: ClientWindsurf,
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
			},
			fixture:    `{"mcpServers":{"remote":{"type":"http","url":"https://mcp.example.com/api"}}}`,
			wantServer: "remote", wantHost: "https://mcp.example.com",
		},
		{
			name:   "codex global (TOML)",
			client: ClientCodex,
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".codex", "config.toml")
			},
			fixture: "[mcp_servers.github]\ncommand = \"npx\"\nargs = [\"-y\", \"@modelcontextprotocol/server-github\"]\n",
			wantServer: "github", wantCmd: "npx:-y:@modelcontextprotocol/server-github",
		},
		{
			name:   "gemini global",
			client: ClientGemini,
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".gemini", "settings.json")
			},
			fixture:    `{"mcpServers":{"telara":{"type":"http","url":"https://mcp.telara.example/mcp"}}}`,
			wantServer: "telara", wantHost: "https://mcp.telara.example",
		},
		{
			name:   "amazon-q global",
			client: ClientAmazonQ,
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".aws", "amazonq", "mcp.json")
			},
			fixture:    `{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`,
			wantServer: "github", wantCmd: "npx:-y:@modelcontextprotocol/server-github",
		},
		{
			name:   "vscode project (provisional fallback)",
			client: ClientVSCode,
			scope:  ScopeProject,
			configPath: func(home, cwd string) string {
				return filepath.Join(cwd, ".vscode", "mcp.json")
			},
			// VS Code nests servers under "servers", not "mcpServers".
			fixture:    `{"servers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`,
			wantServer: "github", wantCmd: "npx:-y:@modelcontextprotocol/server-github",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := t.TempDir()
			t.Setenv("HOME", home)
			t.Chdir(cwd)

			path := c.configPath(home, cwd)
			writeFixture(t, path, c.fixture)

			result := Scan(c.client, c.scope)
			requireStatus(t, result, ScanOK)
			if len(result.Servers) != 1 {
				t.Fatalf("expected 1 server, got %d: %+v", len(result.Servers), result.Servers)
			}
			server := result.Servers[0]
			if server.ServerName != c.wantServer {
				t.Fatalf("server name = %q, want %q", server.ServerName, c.wantServer)
			}
			if c.wantHost != "" && server.EndpointHost != c.wantHost {
				t.Fatalf("endpoint host = %q, want %q", server.EndpointHost, c.wantHost)
			}
			if c.wantCmd != "" && server.CommandIdentity != c.wantCmd {
				t.Fatalf("command identity = %q, want %q", server.CommandIdentity, c.wantCmd)
			}
		})
	}

	// Negative case: a config file at a location NO source scans (a made-up
	// scope name) must never surface as a match — proves the catalog-driven
	// engine only matches declared (client_id, scope) pairs, not "any file
	// that happens to parse."
	t.Run("unscanned location does not match", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeFixture(t, filepath.Join(home, ".claude", "decoy-scope.json"), `{"mcpServers":{"ghost":{"command":"npx"}}}`)

		result := Scan(ClientClaudeCode, "decoy-scope")
		if result.Status != ScanUnsupported {
			t.Fatalf("expected ScanUnsupported for an undeclared scope, got %q", result.Status)
		}
		if len(result.Servers) != 0 {
			t.Fatalf("expected no servers from an unscanned location, got %+v", result.Servers)
		}
	})
}

// TestTENG2225NewFileModeVendorsExtractRealistically proves the file-mode
// ai_estate blocks TENG-2225 added to the real embedded catalog (batch 1:
// cline.yaml, factory.yaml; batch 2: sourcegraph_cody.yaml/Amp) extract
// correctly against a fixture shaped like that vendor's actual documented
// config file — not merely well-formed YAML that happens to load. Driven
// through Scan() -> fileSources() -> the real embedded production catalog,
// exactly like TestParityWithHardcodedScanner above, just for client_id
// strings that have no ClientXxx constant (Scan matches on the raw
// client_id/scope strings the catalog declares, so none is needed).
//
// Sources, verified 2026-08-17:
//   - Cline: https://docs.cline.bot/mcp/configuring-mcp-servers — CLI global
//     config at ~/.cline/mcp.json, mcpServers object (command/args or
//     url/type), same shape as cursor/windsurf/claude_code.
//   - Factory: https://docs.factory.ai/cli/configuration/mcp — Droid CLI
//     mcp.json at user (~/.factory/mcp.json) and project (.factory/mcp.json)
//     scope, top-level mcpServers object with explicit "type": "stdio"/"http".
//   - Amp (sourcegraph_cody.yaml): https://ampcode.com/manual — user settings
//     at ~/.config/amp/settings.json(c), workspace settings at the nearest
//     .amp/settings.json(c) searched upward from cwd. MCP servers live under
//     the literal top-level key "amp.mcpServers" (a VS-Code-style dotted
//     setting name, NOT a nested {amp:{mcpServers:{}}} object) — the catalog
//     source's items_path is the literal string "amp.mcpServers", which
//     exercises mapValue()'s exact-key lookup (no dot-path traversal) rather
//     than the plain top-level "mcpServers" every other vendor here uses.
func TestTENG2225NewFileModeVendorsExtractRealistically(t *testing.T) {
	type tc struct {
		name       string
		client     string
		scope      string
		configPath func(home, cwd string) string
		fixture    string
		wantServer string
		wantHost   string
		wantCmd    string
		wantScope  string
	}

	cases := []tc{
		{
			name:   "cline global",
			client: "cline",
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".cline", "mcp.json")
			},
			// Shape straight from docs.cline.bot: mcpServers keyed by server
			// name, stdio entries carry command/args.
			fixture:    `{"mcpServers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`,
			wantServer: "github", wantCmd: "npx:-y:@modelcontextprotocol/server-github",
		},
		{
			name:   "factory user level",
			client: "factory",
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".factory", "mcp.json")
			},
			// Shape from docs.factory.ai's HTTP example: explicit "type": "http".
			fixture:    `{"mcpServers":{"linear":{"type":"http","url":"https://mcp.linear.app/mcp","disabled":false}}}`,
			wantServer: "linear", wantHost: "https://mcp.linear.app",
		},
		{
			name:   "factory project level",
			client: "factory",
			scope:  ScopeProject,
			configPath: func(home, cwd string) string {
				return filepath.Join(cwd, ".factory", "mcp.json")
			},
			// Shape from docs.factory.ai's stdio example: explicit "type": "stdio".
			fixture:    `{"mcpServers":{"my-server":{"type":"stdio","command":"npx","args":["-y","@some/mcp-server"],"disabledTools":["unused_tool"]}}}`,
			wantServer: "my-server", wantCmd: "npx:-y:@some/mcp-server",
		},
		{
			name:   "amp global (sourcegraph_cody.yaml)",
			client: "amp",
			scope:  ScopeGlobal,
			configPath: func(home, cwd string) string {
				return filepath.Join(home, ".config", "amp", "settings.json")
			},
			// Shape straight from ampcode.com/manual: a general VS-Code-style
			// settings.json carrying other amp.* keys alongside the literal
			// "amp.mcpServers" key; local server entries carry command/args,
			// no "type" discriminator (verifies the value_map "": stdio
			// default, not a literal type field).
			fixture:    `{"amp.notifications.enabled":true,"amp.mcpServers":{"playwright":{"command":"npx","args":["-y","@playwright/mcp@latest","--headless"]}}}`,
			// NormalizeCommandIdentity (privacy.go) stops at the first safe
			// package identity token, so the trailing --headless flag (after
			// the package arg) is intentionally not captured.
			wantServer: "playwright", wantCmd: "npx:-y:@playwright/mcp@latest",
		},
		{
			name:   "amp workspace (sourcegraph_cody.yaml)",
			client: "amp",
			scope:  ScopeProject,
			configPath: func(home, cwd string) string {
				return filepath.Join(cwd, ".amp", "settings.json")
			},
			// Shape from ampcode.com/manual's remote-server example: url +
			// headers, again no "type" field.
			fixture:    `{"amp.mcpServers":{"sourcegraph":{"url":"https://sourcegraph.example.com/.api/mcp/v1","headers":{"Authorization":"token xyz"}}}}`,
			wantServer: "sourcegraph", wantHost: "https://sourcegraph.example.com",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := t.TempDir()
			t.Setenv("HOME", home)
			t.Chdir(cwd)

			path := c.configPath(home, cwd)
			writeFixture(t, path, c.fixture)

			result := Scan(c.client, c.scope)
			requireStatus(t, result, ScanOK)
			if len(result.Servers) != 1 {
				t.Fatalf("expected 1 server, got %d: %+v", len(result.Servers), result.Servers)
			}
			server := result.Servers[0]
			if server.ServerName != c.wantServer {
				t.Fatalf("server name = %q, want %q", server.ServerName, c.wantServer)
			}
			if c.wantHost != "" && server.EndpointHost != c.wantHost {
				t.Fatalf("endpoint host = %q, want %q", server.EndpointHost, c.wantHost)
			}
			if c.wantCmd != "" && server.CommandIdentity != c.wantCmd {
				t.Fatalf("command identity = %q, want %q", server.CommandIdentity, c.wantCmd)
			}

			// mcp_client_configuration record must also resolve (the OTHER
			// kind this same file read yields, per schema doc §6a) — proves
			// the load-bearing resource, not just the server list, extracts.
			var sawClientConfig bool
			for _, rec := range result.records {
				if rec.Kind == "mcp_client_configuration" {
					sawClientConfig = true
					if rec.Fields["client"] != c.client {
						t.Errorf("mcp_client_configuration client field = %q, want %q", rec.Fields["client"], c.client)
					}
					if rec.Fields["scope"] != c.scope {
						t.Errorf("mcp_client_configuration scope field = %q, want %q", rec.Fields["scope"], c.scope)
					}
				}
			}
			if !sawClientConfig {
				t.Fatalf("expected an mcp_client_configuration record alongside the server, got records: %+v", result.records)
			}
		})
	}

	// Negative: an undeclared (client, scope) pair for these two vendors must
	// stay unsupported, not silently fall through to some other vendor's path.
	t.Run("cline project is not a declared source", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		result := Scan("cline", ScopeProject)
		if result.Status != ScanUnsupported {
			t.Fatalf("expected ScanUnsupported for cline/project (only global is catalogued), got %q", result.Status)
		}
	})

	// Negative: amp/managed is not a declared scope (only global and project
	// are authored on sourcegraph_cody.yaml) — must not silently fall through
	// to another vendor's or another scope's source.
	t.Run("amp managed is not a declared source", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		result := Scan("amp", ScopeManaged)
		if result.Status != ScanUnsupported {
			t.Fatalf("expected ScanUnsupported for amp/managed (only global and project are catalogued), got %q", result.Status)
		}
	})
}

// TestContinueDevYAMLConfigExtractsRealistically is TENG-2225 Batch E's
// follow-up test for continue_dev.yaml, and it is pinning down two separate
// facts at once, both real regressions this codebase has hit before:
//
//  1. parseConfig's new YAML branch decodes into map[string]interface{} for a
//     REAL config shape end to end — through Scan() -> the real embedded
//     catalog -> parseConfig -> walkResource -> mapValue()'s
//     raw.(map[string]interface{}) assertion — not a standalone
//     yaml.Unmarshal call. yaml.v3 only picks stringMapType (rather than the
//     map[interface{}]interface{} some other YAML libraries always use) when
//     every key in a mapping node is itself a string; a fixture with a
//     mapping value nested under a list element (exactly what mcpServers'
//     list-of-objects shape requires) is what actually exercises that path.
//     A silent map[interface{}]interface{} regression here would parse
//     clean and extract zero servers — "well-formed is not working."
//  2. continue_dev.yaml's ai_estate block matches Continue's REAL documented
//     config.yaml shape (docs.continue.dev/customize/deep-dives/mcp,
//     verified 2026-08-17): mcpServers is a LIST of {name, command/args or
//     url+type, ...} objects, unlike every other file-mode vendor in this
//     catalog (cursor/claude-code/sourcegraph_cody all use a name-keyed MAP).
//     Authoring the block against the wrong (map) shape — the shape every
//     precedent in this file uses — would have parsed fine and silently
//     extracted nothing; walkResourceListItems (scanner.go) exists because
//     of that gap, not just the YAML one.
func TestContinueDevYAMLConfigExtractsRealistically(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Shape straight from docs.continue.dev: a local stdio server (command +
	// args, no "type" — must default to stdio via the value_map) and a
	// remote sse server (url + explicit "type"), covering both entry shapes
	// config.yaml documents.
	fixture := "mcpServers:\n" +
		"  - name: SQLite\n" +
		"    command: npx\n" +
		"    args:\n" +
		"      - \"@modelcontextprotocol/server-sqlite\"\n" +
		"      - \"/path/to/database.db\"\n" +
		"  - name: Remote Docs\n" +
		"    type: sse\n" +
		"    url: https://example.com/mcp\n"
	writeFixture(t, filepath.Join(home, ".continue", "config.yaml"), fixture)

	result := Scan("continue_dev", ScopeGlobal)
	requireStatus(t, result, ScanOK)

	if len(result.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d: %+v", len(result.Servers), result.Servers)
	}
	byName := map[string]DiscoveredServer{}
	for _, s := range result.Servers {
		byName[s.ServerName] = s
	}

	sqlite, ok := byName["SQLite"]
	if !ok {
		t.Fatalf("missing SQLite server, got: %+v", result.Servers)
	}
	if sqlite.Transport != TransportStdio {
		t.Errorf("SQLite transport = %q, want %q (no explicit type — value_map default)", sqlite.Transport, TransportStdio)
	}
	// NormalizeCommandIdentity stops at the first safe package identity
	// token (privacy.go), so the trailing db-path arg is intentionally not
	// captured — mirrors the amp/playwright case in the test above.
	if sqlite.CommandIdentity != "npx:@modelcontextprotocol/server-sqlite" {
		t.Errorf("SQLite command identity = %q, want %q", sqlite.CommandIdentity, "npx:@modelcontextprotocol/server-sqlite")
	}

	docs, ok := byName["Remote Docs"]
	if !ok {
		t.Fatalf("missing Remote Docs server, got: %+v", result.Servers)
	}
	if docs.Transport != TransportSSE {
		t.Errorf("Remote Docs transport = %q, want %q", docs.Transport, TransportSSE)
	}
	if docs.EndpointHost != "https://example.com" {
		t.Errorf("Remote Docs endpoint host = %q, want %q", docs.EndpointHost, "https://example.com")
	}

	// mcp_client_configuration must also resolve alongside the two servers —
	// the OTHER kind this same file read yields (schema doc §6a).
	var sawClientConfig bool
	for _, rec := range result.records {
		if rec.Kind == "mcp_client_configuration" {
			sawClientConfig = true
			if rec.Fields["client"] != "continue_dev" {
				t.Errorf("mcp_client_configuration client field = %q, want %q", rec.Fields["client"], "continue_dev")
			}
			if rec.Fields["scope"] != ScopeGlobal {
				t.Errorf("mcp_client_configuration scope field = %q, want %q", rec.Fields["scope"], ScopeGlobal)
			}
		}
	}
	if !sawClientConfig {
		t.Fatalf("expected an mcp_client_configuration record alongside the servers, got records: %+v", result.records)
	}

	// Negative: project scope is not a declared source for continue_dev (the
	// task anchored specifically on the confirmed global config.yaml; a
	// workspace-level config was never verified and must not silently exist).
	t.Run("continue_dev project is not a declared source", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		result := Scan("continue_dev", ScopeProject)
		if result.Status != ScanUnsupported {
			t.Fatalf("expected ScanUnsupported for continue_dev/project (only global is catalogued), got %q", result.Status)
		}
	})
}

// TestOneConfigFileYieldsClientConfigAndServers proves the multi-kind case
// the schema doc's §6a exists to demonstrate: one .claude.json read produces
// ONE mcp_client_configuration record plus one mcp_server_deployment record
// per entry under mcpServers — and the underlying file is read exactly once
// regardless of how many resources or servers it yields.
func TestOneConfigFileYieldsClientConfigAndServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFixture(t, filepath.Join(home, ".claude.json"), `{
		"mcpServers": {
			"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"]},
			"internal": {"url": "https://mcp.acme.example/sse"}
		}
	}`)

	reads := 0
	original := readConfigFile
	readConfigFile = func(path string) ([]byte, error) {
		reads++
		return original(path)
	}
	t.Cleanup(func() { readConfigFile = original })

	result := Scan(ClientClaudeCode, ScopeGlobal)
	requireStatus(t, result, ScanOK)

	if reads != 1 {
		t.Fatalf("expected exactly 1 file read, got %d", reads)
	}

	var clientConfigCount, serverCount int
	for _, rec := range result.records {
		switch rec.Kind {
		case "mcp_client_configuration":
			clientConfigCount++
		case "mcp_server_deployment":
			serverCount++
		default:
			t.Fatalf("unexpected resource kind %q", rec.Kind)
		}
	}
	if clientConfigCount != 1 {
		t.Fatalf("expected 1 mcp_client_configuration record, got %d", clientConfigCount)
	}
	if serverCount != 2 {
		t.Fatalf("expected 2 mcp_server_deployment records (one per mcpServers entry), got %d", serverCount)
	}
	if len(result.Servers) != 2 {
		t.Fatalf("expected 2 DiscoveredServer entries surfaced to the wire model, got %d", len(result.Servers))
	}
}

// TestSkillScopesRemainSeparateBoundary proves skill roots stay a distinct
// collection boundary from MCP-config scopes: an absent skills directory
// (ScanFileAbsent — COMPLETE, tombstone-authorizing) must never affect the
// independently-scanned MCP configuration scope for the same client, and the
// two must never collide on one scope key.
func TestSkillScopesRemainSeparateBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// MCP config IS present …
	writeFixture(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"github":{"command":"npx"}}}`)
	// … but the skills directory is NOT. If skills and MCP config shared one
	// scope key, this would look like a partial/contradictory scan of the
	// same boundary; they must not.

	mcpResult := Scan(ClientClaudeCode, ScopeGlobal)
	requireStatus(t, mcpResult, ScanOK)
	if len(mcpResult.Servers) != 1 {
		t.Fatalf("expected 1 server from the present MCP config, got %d", len(mcpResult.Servers))
	}

	skillResults := ScanSkills()
	var globalSkills *ConfigScanResult
	for i := range skillResults {
		if skillResults[i].ClientFamily == ClientClaudeCode && skillResults[i].Scope == ScopeGlobalSkills {
			globalSkills = &skillResults[i]
		}
	}
	if globalSkills == nil {
		t.Fatal("expected a global-skills scan result for claude-code")
	}
	if globalSkills.Status != ScanFileAbsent {
		t.Fatalf("expected ScanFileAbsent for the missing skills root, got %q", globalSkills.Status)
	}

	// The scope keys must be distinct — ScopeGlobal != ScopeGlobalSkills —
	// and BuildReport must carry both without either one's status leaking
	// into the other's CollectionScope entry.
	if mcpResult.Scope == globalSkills.Scope {
		t.Fatalf("MCP config scope and skills scope collided: both %q", mcpResult.Scope)
	}
	report := BuildReport(testCollector(), "scan-boundary-1", fixedStarted, fixedCompleted, []ConfigScanResult{mcpResult, *globalSkills})
	byScope := map[string]CollectionScope{}
	for _, s := range report.Scopes {
		byScope[s.SourceScope] = s
	}
	mcpScope, ok := byScope["claude-code:global"]
	if !ok {
		t.Fatal("missing claude-code:global scope in report")
	}
	skillScope, ok := byScope["claude-code:global-skills"]
	if !ok {
		t.Fatal("missing claude-code:global-skills scope in report")
	}
	if mcpScope.CollectionStatus != WireCollectionComplete {
		t.Fatalf("MCP config scope status = %q, want COMPLETE (it was found and read)", mcpScope.CollectionStatus)
	}
	if skillScope.CollectionStatus != WireCollectionComplete {
		t.Fatalf("skills scope status = %q, want COMPLETE (absence is a complete answer)", skillScope.CollectionStatus)
	}
	// The absent skills scope must not have tombstoned or otherwise emptied
	// the MCP scope's own, independently-discovered server.
	if len(report.ResourceAssertions) != 1 {
		t.Fatalf("expected 1 assertion (the MCP server; skills scope found none), got %d: %+v",
			len(report.ResourceAssertions), report.ResourceAssertions)
	}
}

// TestNonFileSourcesSkippedSilently proves the engine loop's second property
// (schema doc §7): tool and workload sources are invisible to the CLI
// without erroring. The real embedded catalog has real discover:tool sources
// today (e.g. slack.yaml) — this drives the assertion against them directly
// rather than a hand-built fixture.
func TestNonFileSourcesSkippedSilently(t *testing.T) {
	specs, err := catalog.LoadIntegrationsFromFS(catalog.EmbeddedFS)
	if err != nil {
		t.Fatalf("loading embedded catalog: %v", err)
	}
	toolIntegration := ""
	for name, spec := range specs {
		if spec.AIEstate == nil {
			continue
		}
		for _, src := range spec.AIEstate.Sources {
			if src.EffectiveDiscover() == catalog.AIEstateDiscoverTool {
				toolIntegration = name
			}
		}
	}
	if toolIntegration == "" {
		t.Fatal("expected at least one discover:tool ai_estate source in the embedded catalog to test against")
	}

	sources, err := loadCatalogFileSourcesFromFS(catalog.EmbeddedFS)
	if err != nil {
		t.Fatalf("loadCatalogFileSourcesFromFS: %v", err)
	}
	for _, s := range sources {
		if s.IntegrationName == toolIntegration {
			t.Fatalf("discover:tool integration %q leaked into the file-source engine", toolIntegration)
		}
	}
}

// TestScannerWorksOffline proves ScanAll() makes no network call on any
// path: the catalog is compiled in via go:embed and every config read is
// local disk I/O. http.DefaultTransport is replaced with one that fails the
// test the instant anything tries to dial out.
func TestScannerWorksOffline(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = explodingTransport{t}
	t.Cleanup(func() { http.DefaultTransport = original })

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFixture(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"github":{"command":"npx"}}}`)

	results := ScanAll()
	if len(results) == 0 {
		t.Fatal("expected at least one scan result")
	}
}

type explodingTransport struct{ t *testing.T }

func (e explodingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	e.t.Fatalf("unexpected network call during offline scan: %s %s", req.Method, req.URL)
	return nil, nil
}

// TestUnresolvableIdentityDroppedAndCounted proves §7 property 3: a record
// whose identity cannot be resolved (id_field empty, no id_fallback_fields
// match) is DROPPED and COUNTED, never silently absent. Every shipped
// production id_field is a pseudo-field (_path/_key) that is always
// non-empty by construction, so this drives a small synthetic catalog
// (real YAML text through the real loader, via an in-memory fs.FS standing
// in for definitions/) whose resource identifies servers by a real,
// sometimes-missing field instead.
func TestUnresolvableIdentityDroppedAndCounted(t *testing.T) {
	const def = `
name: drop_fixture_client
display_name: Drop Fixture Client
description: Synthetic fixture integration exercising id_field/id_fallback_fields.
api:
  type: mcp
ai_estate:
  sources:
    - substrate: endpoint
      discover: file
      client_id: drop-fixture
      scope: global
      config_paths: [".dropfixture/config.json"]
      resources:
        - kind: mcp_server_deployment
          items_path: mcpServers
          id_field: server_id
          id_fallback_fields: [alt_id]
          id_fallback_prefix: "fallback:"
          fields:
            name: _key
`
	fsys := fstest.MapFS{
		"definitions/drop_fixture_client.yaml": &fstest.MapFile{Data: []byte(def)},
	}

	sources, err := loadCatalogFileSourcesFromFS(fsys)
	if err != nil {
		t.Fatalf("loadCatalogFileSourcesFromFS: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 file source from the fixture catalog, got %d", len(sources))
	}

	cwd := t.TempDir()
	t.Chdir(cwd)
	writeFixture(t, filepath.Join(cwd, ".dropfixture", "config.json"), `{
		"mcpServers": {
			"a": {"server_id": "srv-a"},
			"b": {"alt_id": "srv-b"},
			"c": {"unrelated": "no identity here"}
		}
	}`)

	result := scanCatalogSource(sources[0])
	requireStatus(t, result, ScanOK)

	if result.DroppedRecords != 1 {
		t.Fatalf("expected 1 dropped record (entry %q has no server_id and no alt_id), got %d", "c", result.DroppedRecords)
	}

	var ids []string
	for _, rec := range result.records {
		ids = append(ids, rec.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 resolved records, got %d: %v", len(ids), ids)
	}
	foundPrimary, foundFallback := false, false
	for _, id := range ids {
		if id == "srv-a" {
			foundPrimary = true
		}
		if id == "fallback:srv-b" {
			foundFallback = true
		}
	}
	if !foundPrimary {
		t.Fatalf("expected the primary id_field match (srv-a) to resolve, got ids %v", ids)
	}
	if !foundFallback {
		t.Fatalf("expected the id_fallback_fields match, prefixed with id_fallback_prefix (fallback:srv-b), got ids %v", ids)
	}
}

// TestRealEmbeddedCatalogFileSourcesAreExecuted is the proof telara-
// utilities' (currently held-out) discover-mode reachability test demands
// before AIEstateModeOwner[AIEstateDiscoverFile] may name this engine: every
// discover:file source shipped in the real embedded catalog is found by
// loadCatalogFileSourcesFromFS AND actually executes to a scan result — not
// merely parsed and never read. See catalog.go's package doc for the full
// context and this change's final report for whether the claim was made.
func TestRealEmbeddedCatalogFileSourcesAreExecuted(t *testing.T) {
	specs, err := catalog.LoadIntegrationsFromFS(catalog.EmbeddedFS)
	if err != nil {
		t.Fatalf("loading embedded catalog: %v", err)
	}
	var shippedFileSources int
	for _, spec := range specs {
		if spec.AIEstate == nil {
			continue
		}
		for _, src := range spec.AIEstate.Sources {
			if src.EffectiveDiscover() == catalog.AIEstateDiscoverFile {
				shippedFileSources++
			}
		}
	}
	if shippedFileSources == 0 {
		t.Fatal("expected the embedded catalog to ship at least one discover:file source")
	}

	sources, err := loadCatalogFileSourcesFromFS(catalog.EmbeddedFS)
	if err != nil {
		t.Fatalf("loadCatalogFileSourcesFromFS: %v", err)
	}
	if len(sources) != shippedFileSources {
		t.Fatalf("engine found %d file sources, catalog shipped %d — some source was declared but never reached the engine",
			len(sources), shippedFileSources)
	}

	// Not merely FOUND — EXECUTED: every one of them must produce a real,
	// well-formed ConfigScanResult when run against an empty home/cwd (the
	// common "developer laptop with nothing configured yet" case). A source
	// that panics, errors, or returns a zero-value/garbage result here would
	// be exactly the "parses, validates, and is never meaningfully read"
	// defect this test exists to catch.
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)

	for _, src := range sources {
		if _, shipped := specs[src.IntegrationName]; !shipped {
			continue // e.g. the vscode provisional fallback — not a catalog-shipped source
		}
		result := scanCatalogSource(src)
		if result.ClientFamily != src.ClientID || result.Scope != src.Scope {
			t.Fatalf("source %s/%s: scan result identity mismatch: %+v", src.ClientID, src.Scope, result)
		}
		if result.Status != ScanFileAbsent {
			t.Fatalf("source %s/%s: expected ScanFileAbsent against an empty home/cwd, got %q (status: %s)",
				src.ClientID, src.Scope, result.Status, result.ErrorClass)
		}
	}
}

// explodingReadFile is unused directly but documents that readConfigFile
// (scanner.go) is the only disk/network seam the engine has — kept here so
// a future reader grepping for "network" in this test file finds the intent
// even though the assertion above is the load-bearing one.
var _ = os.ReadFile
