package discovery

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	catalog "gitlab.com/telara-labs/telara-utilities/go/integrations/catalog"
	"gopkg.in/yaml.v3"
)

// catalog.go loads the ai_estate `discover: file` sources declared in the
// telara-utilities integration catalog and turns them into the client/scope/
// path/resource tables scanner.go used to hardcode (TENG-2218). See
// telara-documentation/architecture/ai-estate-catalog-schema.md §7 ("the
// engine loop") for the executable contract implemented here, and §2 for why
// `discover: file` belongs to the CLI (`tool` and `workload` belong to
// knowledge-service and are skipped silently — see loadCatalogFileSources).
//
// SCHEMA GAP (TENG-2218, found while implementing this — reported, not
// fixed, per the "stay inside telara-cli" boundary on this change):
// telara-utilities' AIEstateSourceSpec / AIEstateResourceSpec Go types
// (go/integrations/catalog/types.go, as of commit 4e86f805) have NOT yet
// landed the fields this engine needs to identify and execute a file source:
//
//   - source-level:   discover, client_id, scope, config_paths
//   - resource-level: load_bearing
//
// Only kind, resources[], per-source substrate, and the resource id/fields/
// value_map machinery have landed (commit d725e4c2, "make ai_estate resource
// kind catalog data"). The catalog YAML itself already declares
// discover/client_id/scope/config_paths for all six file-discovery vendors
// currently authored (claude_code.yaml, cursor.yaml, windsurf.yaml,
// openai_codex.yaml, gemini_code_assist.yaml, amazon_q.yaml) — authored
// ahead of the engine, which the schema doc's "Inert-YAML rule" explicitly
// permits ("Authoring ahead of the engine is allowed; authoring ahead
// silently is not" — and discover:file IS listed as pending in §2's table,
// so this is not a silent case). But yaml.Unmarshal into the CURRENT typed
// struct drops these fields silently, because the struct has no field to
// receive them.
//
// This file therefore re-parses the SAME embedded bytes into rawAIEstate*
// structs shaped to match the schema doc exactly, rather than reading these
// fields off catalog.AIEstateSourceSpec. telara-utilities is out of scope
// for this change (other agents are concurrently editing it) — the upstream
// field additions this unblocks should be filed as a TENG-2218 follow-up.
// Once they land, rawAIEstate* below should be deleted and replaced with
// direct field access on catalog.AIEstateSourceSpec / AIEstateResourceSpec.
//
// catalog.LoadIntegrationsFromFS(catalog.EmbeddedFS) IS still called below —
// its per-integration validation (resource-kind vocabulary against the
// canonical catalog.AIEstateValidResourceKinds table, kind/resources mutual
// exclusivity, duplicate-name detection, malformed-YAML skip-and-log) is
// real and reused rather than re-implemented: an integration is only
// eligible for file-source execution here if it ALSO loaded cleanly through
// the shared, validated loader (see the validated-name cross-check in
// loadCatalogFileSources).
//
// SECOND GAP found while implementing (also reported, not fixed): there is
// no vscode.yaml (or any ai_estate source naming client_id "vscode")
// anywhere under go/integrations/catalog/definitions — confirmed by grep.
// VS Code is one of the 7 clients the scanner this replaces detected. See
// vsCodeFallbackSource below for how this migration avoids regressing VS
// Code detection without authoring new catalog content (also out of scope
// here).

// rawFieldSource mirrors catalog.AIEstateFieldSource: a destination may name
// one path (scalar) or several (list — tried in order for scalars,
// concatenated for list-valued sources; see extractField).
type rawFieldSource []string

func (f *rawFieldSource) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var single string
		if err := node.Decode(&single); err != nil {
			return err
		}
		*f = rawFieldSource{single}
		return nil
	case yaml.SequenceNode:
		var many []string
		if err := node.Decode(&many); err != nil {
			return err
		}
		*f = many
		return nil
	default:
		return fmt.Errorf("ai_estate field source must be a string or a list of strings")
	}
}

// rawAIEstateResource mirrors catalog.AIEstateResourceSpec PLUS the
// load_bearing field that struct is missing (see package doc above).
type rawAIEstateResource struct {
	Kind             string                    `yaml:"kind"`
	ItemsPath        string                    `yaml:"items_path"`
	IDField          string                    `yaml:"id_field"`
	IDFallbackPrefix string                    `yaml:"id_fallback_prefix"`
	IDFallbackFields []string                  `yaml:"id_fallback_fields"`
	Fields           map[string]rawFieldSource `yaml:"fields"`
	ValueMap         map[string]map[string]string `yaml:"value_map"`
	LoadBearing      bool                      `yaml:"load_bearing"`
}

// rawAIEstateSource mirrors catalog.AIEstateSourceSpec PLUS the discover,
// client_id, scope and config_paths fields that struct is missing (see
// package doc above).
type rawAIEstateSource struct {
	Substrate   string                `yaml:"substrate"`
	Discover    string                `yaml:"discover"`
	ClientID    string                `yaml:"client_id"`
	Scope       string                `yaml:"scope"`
	ConfigPaths []string              `yaml:"config_paths"`
	Resources   []rawAIEstateResource `yaml:"resources"`
}

type rawAIEstateBlock struct {
	Substrate string              `yaml:"substrate"`
	Sources   []rawAIEstateSource `yaml:"sources"`
}

// rawIntegrationDoc reads only the two top-level fields this engine needs
// (name, ai_estate) out of an otherwise much larger integration definition —
// everything else (auth, tools, spend_connector, ...) is irrelevant here and
// is left for yaml.Unmarshal to silently ignore.
type rawIntegrationDoc struct {
	Name     string            `yaml:"name"`
	AIEstate *rawAIEstateBlock `yaml:"ai_estate"`
}

// catalogFileSource is one discover:file source, resolved and ready for the
// engine loop in scanner.go to execute.
type catalogFileSource struct {
	IntegrationName string
	ClientID        string
	Scope           string
	ConfigPaths     []string
	Resources       []rawAIEstateResource
}

var (
	catalogSourcesOnce sync.Once
	catalogSourcesVal  []catalogFileSource
	catalogSourcesErr  error
)

// fileSources returns every discover:file source declared in the embedded
// catalog (plus the vscode fallback — see vsCodeFallbackSource), computed
// once per process since the embedded catalog is immutable for the lifetime
// of the binary.
func fileSources() ([]catalogFileSource, error) {
	catalogSourcesOnce.Do(func() {
		catalogSourcesVal, catalogSourcesErr = loadCatalogFileSources()
	})
	return catalogSourcesVal, catalogSourcesErr
}

// loadCatalogFileSources implements the discovery-mode half of the schema
// doc's §7 engine loop: "for each integration in catalog ... for each source
// in integration.ai_estate.sources ... if substrate not executable by THIS
// engine: skip # CLI takes file, knowledge takes tool+workload". Everything
// downstream in scanner.go implements the rest of the loop (one read per
// source, many resources per read).
func loadCatalogFileSources() ([]catalogFileSource, error) {
	// Reused validation: an integration only contributes file sources here if
	// it ALSO loads cleanly through telara-utilities' own validated loader
	// (resource-kind vocabulary, kind/resources exclusivity, duplicate names,
	// malformed-YAML skip-and-log). See package doc above.
	validated, err := catalog.LoadIntegrationsFromFS(catalog.EmbeddedFS)
	if err != nil {
		return nil, fmt.Errorf("loading embedded catalog: %w", err)
	}

	entries, err := fs.ReadDir(catalog.EmbeddedFS, "definitions")
	if err != nil {
		return nil, fmt.Errorf("reading embedded catalog definitions: %w", err)
	}

	var sources []catalogFileSource
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}

		data, err := fs.ReadFile(catalog.EmbeddedFS, "definitions/"+name)
		if err != nil {
			return nil, fmt.Errorf("reading embedded %s: %w", name, err)
		}

		var doc rawIntegrationDoc
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parsing embedded %s: %w", name, err)
		}
		if doc.AIEstate == nil {
			continue
		}
		if _, ok := validated[doc.Name]; !ok {
			// This integration failed the shared loader's validation (or
			// isn't present under the name this file declares) and was
			// already skip-logged there. Do not double-report; just exclude
			// its ai_estate sources from execution.
			continue
		}

		for _, src := range doc.AIEstate.Sources {
			if src.Discover != "file" {
				// tool and workload sources belong to knowledge-service.
				// Their presence here is not a misconfiguration — schema doc
				// §7 property 2 requires skipping them silently, never
				// erroring.
				continue
			}
			sources = append(sources, catalogFileSource{
				IntegrationName: doc.Name,
				ClientID:        src.ClientID,
				Scope:           src.Scope,
				ConfigPaths:     src.ConfigPaths,
				Resources:       src.Resources,
			})
		}
	}

	sources = append(sources, vsCodeFallbackSource())

	sort.Slice(sources, func(i, j int) bool {
		if sources[i].ClientID != sources[j].ClientID {
			return sources[i].ClientID < sources[j].ClientID
		}
		return sources[i].Scope < sources[j].Scope
	})
	return sources, nil
}

// vsCodeFallbackSource is the one client the catalog does not yet declare.
//
// telara-utilities has no vscode.yaml, and no ai_estate source anywhere
// names client_id "vscode" — confirmed by grep across
// go/integrations/catalog/definitions/*.yaml. Every other one of the 7
// clients the scanner this replaces used to detect has a real catalog row;
// this is the one gap. It is authored here, in Go, rather than silently
// dropping VS Code detection — the task this change was written against is
// explicit that "any client that stops being detected is a regression, not
// a cleanup." Authoring go/integrations/catalog/definitions/vscode.yaml is
// out of scope for a telara-cli-only change; this function should be deleted
// once that lands upstream (TENG-2218 follow-up).
//
// Shape matches the doc's own worked example (§6a) exactly: one file yields
// both mcp_client_configuration and mcp_server_deployment. VS Code's own
// mcp.json nests servers under "servers", not "mcpServers" — the one place
// VS Code's file shape actually differs from the other six.
func vsCodeFallbackSource() catalogFileSource {
	return catalogFileSource{
		IntegrationName: "vscode",
		ClientID:        ClientVSCode,
		Scope:           ScopeProject,
		ConfigPaths:     []string{".vscode/mcp.json"},
		Resources: []rawAIEstateResource{
			{
				Kind:    "mcp_client_configuration",
				IDField: "_path",
				Fields: map[string]rawFieldSource{
					"client": {"_client_id"},
					"scope":  {"_scope"},
				},
				LoadBearing: true,
			},
			{
				Kind:      "mcp_server_deployment",
				ItemsPath: "servers",
				IDField:   "_key",
				Fields: map[string]rawFieldSource{
					"name":      {"_key"},
					"command":   {"command", "args"},
					"transport": {"type"},
				},
				ValueMap: map[string]map[string]string{
					"transport": {"": "stdio"},
				},
			},
		},
	}
}
