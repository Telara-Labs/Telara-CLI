package discovery

import (
	"fmt"
	"io/fs"
	"sort"
	"sync"

	catalog "gitlab.com/telara-labs/telara-utilities/go/integrations/catalog"
)

// catalog.go loads the ai_estate `discover: file` sources declared in the
// telara-utilities integration catalog and turns them into the client/scope/
// path/resource tables scanner.go used to hardcode (TENG-2218). See
// telara-documentation/architecture/ai-estate-catalog-schema.md §7 ("the
// engine loop") for the executable contract implemented here, and §2 for why
// `discover: file` belongs to the CLI (`tool` and `workload` belong to
// knowledge-service and are skipped silently — see loadCatalogFileSourcesFromFS).
//
// This reads catalog.AIEstateSourceSpec / catalog.AIEstateResourceSpec
// directly via catalog.LoadIntegrationsFromFS(catalog.EmbeddedFS) — no
// bespoke YAML parsing. An earlier draft of this file carried a hand-rolled
// raw-YAML shadow of these types because, at the time of writing, telara-
// utilities' typed structs had not yet landed `discover` / `client_id` /
// `scope` / `config_paths` (source level) or `load_bearing` (resource
// level). Commits 843129c4 ("add discover modes and mode-specific fields to
// the ai_estate schema"), 89a1a847 ("Author AI estate catalog blocks for 6
// coding clients"), 9c16d8a6 ("reject source-level extraction on a
// resources[] source"), and 43875d69 ("resource-level load_bearing") landed
// all of it, so that bridge was deleted in favor of the typed API the task
// originally asked for.
//
// telara-utilities also tracks WHICH engine executes each discover mode
// (catalog.AIEstateModeOwner / catalog.ModeIsExecuted). AIEstateDiscoverFile
// should only be claimed there as this engine's identity once it genuinely
// executes shipped file sources end to end — see
// TestRealEmbeddedCatalogFileSourcesAreExecuted in catalog_engine_test.go,
// which is the proof telara-utilities' reachability test demands. That map
// lives in telara-utilities and is out of scope for a telara-cli-only
// change; see this change's final report for whether the claim is warranted
// yet.

// catalogFileSource is one discover:file source, resolved and ready for the
// engine loop in scanner.go to execute.
type catalogFileSource struct {
	IntegrationName string
	ClientID        string
	Scope           string
	ConfigPaths     []string
	Resources       []catalog.AIEstateResourceSpec
}

var (
	catalogSourcesOnce sync.Once
	catalogSourcesVal  []catalogFileSource
	catalogSourcesErr  error
)

// fileSources returns every discover:file source declared in the embedded
// production catalog (plus the vscode fallback and any other client the
// catalog does not yet cover — see provisionalFallbackSources), computed
// once per process since the embedded catalog is immutable for the lifetime
// of the binary.
func fileSources() ([]catalogFileSource, error) {
	catalogSourcesOnce.Do(func() {
		sources, err := loadCatalogFileSourcesFromFS(catalog.EmbeddedFS)
		if err != nil {
			catalogSourcesErr = err
			return
		}
		catalogSourcesVal = withProvisionalFallbacks(sources)
	})
	return catalogSourcesVal, catalogSourcesErr
}

// loadCatalogFileSourcesFromFS implements the discovery-mode half of the
// schema doc's §7 engine loop: "for each integration in catalog ... for each
// source in integration.ai_estate.sources ... if substrate not executable by
// THIS engine: skip # CLI takes file, knowledge takes tool+workload".
// Everything downstream in scanner.go implements the rest of the loop (one
// read per source, many resources per read).
//
// Takes an fs.FS parameter (rather than hardcoding catalog.EmbeddedFS)
// purely so tests can drive the SAME production code path against either the
// real embedded catalog or a small on-disk testdata catalog — see
// catalog_engine_test.go.
func loadCatalogFileSourcesFromFS(fsys fs.FS) ([]catalogFileSource, error) {
	specs, err := catalog.LoadIntegrationsFromFS(fsys)
	if err != nil {
		return nil, fmt.Errorf("loading catalog: %w", err)
	}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	var sources []catalogFileSource
	for _, name := range names {
		spec := specs[name]
		if spec.AIEstate == nil {
			continue
		}
		for _, src := range spec.AIEstate.Sources {
			if src.EffectiveDiscover() != catalog.AIEstateDiscoverFile {
				// tool and workload sources belong to knowledge-service.
				// Their presence here is not a misconfiguration — schema doc
				// §7 property 2 requires skipping them silently, never
				// erroring.
				continue
			}
			sources = append(sources, catalogFileSource{
				IntegrationName: name,
				ClientID:        src.ClientID,
				Scope:           src.Scope,
				ConfigPaths:     src.ConfigPaths,
				Resources:       src.Resources,
			})
		}
	}

	sortCatalogFileSources(sources)
	return sources, nil
}

func sortCatalogFileSources(sources []catalogFileSource) {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].ClientID != sources[j].ClientID {
			return sources[i].ClientID < sources[j].ClientID
		}
		return sources[i].Scope < sources[j].Scope
	})
}

// withProvisionalFallbacks unions provisionalFallbackSources() into the
// catalog-derived list, but ONLY for a (client_id, scope) pair the catalog
// does not already cover. This makes the fallback table self-obsoleting: the
// moment telara-utilities commits and publishes a real ai_estate source for,
// say, vscode/project, this function stops contributing a fallback for it on
// its own — the real catalog row wins, with no code change needed here. Once
// every fallback row is shadowed this way, provisionalFallbackSources can
// simply be deleted.
func withProvisionalFallbacks(sources []catalogFileSource) []catalogFileSource {
	have := make(map[string]bool, len(sources))
	for _, s := range sources {
		have[s.ClientID+"|"+s.Scope] = true
	}
	for _, fallback := range provisionalFallbackSources() {
		if !have[fallback.ClientID+"|"+fallback.Scope] {
			sources = append(sources, fallback)
		}
	}
	sortCatalogFileSources(sources)
	return sources
}

// provisionalFallbackSources covers the one client the catalog does not yet
// declare at all: telara-utilities has no vscode.yaml, and no ai_estate
// source anywhere names client_id "vscode" — confirmed by grep across
// go/integrations/catalog/definitions/*.yaml. Every other one of the 7
// clients the scanner this replaces used to detect now has a real catalog
// row (843129c4 / 89a1a847). This is the one remaining gap. It is authored
// here, in Go, rather than silently dropping VS Code detection — "any client
// that stops being detected is a regression, not a cleanup." Authoring
// go/integrations/catalog/definitions/vscode.yaml is out of scope for a
// telara-cli-only change; this function should be deleted once that lands
// upstream (TENG-2218 follow-up) — withProvisionalFallbacks above already
// stops using any row the catalog starts covering on its own, so deleting
// this function is a pure subtraction whenever that day comes, never a
// coordinated two-repo change.
//
// Shape matches the doc's own worked example (§6a) exactly: one file yields
// both mcp_client_configuration and mcp_server_deployment. VS Code's own
// mcp.json nests servers under "servers", not "mcpServers" — the one place
// VS Code's file shape actually differs from the other six.
func provisionalFallbackSources() []catalogFileSource {
	return []catalogFileSource{
		{
			IntegrationName: "vscode",
			ClientID:        ClientVSCode,
			Scope:           ScopeProject,
			ConfigPaths:     []string{".vscode/mcp.json"},
			Resources: []catalog.AIEstateResourceSpec{
				{
					Kind:    "mcp_client_configuration",
					IDField: "_path",
					Fields: map[string]catalog.AIEstateFieldSource{
						"client": {"_client_id"},
						"scope":  {"_scope"},
					},
					LoadBearing: true,
				},
				{
					Kind:      "mcp_server_deployment",
					ItemsPath: "servers",
					IDField:   "_key",
					Fields: map[string]catalog.AIEstateFieldSource{
						"name":      {"_key"},
						"command":   {"command", "args"},
						"transport": {"type"},
					},
					ValueMap: map[string]map[string]string{
						"transport": {"": "stdio"},
					},
				},
			},
		},
	}
}
