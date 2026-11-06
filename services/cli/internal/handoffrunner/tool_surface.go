package handoffrunner

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The tool surface is what Codex offers the model in a request. It is read from
// the request body itself, the one place every tool Codex has must appear, in
// both forms Codex 0.161.0 sends: a top-level "tools" list, and (for models on
// the lighter wire format) "additional_tools" items inside "input". Namespaces
// are walked, and a tool_search result's tools are read the same way.

// mcpMediated are Codex's own tools that only reach configured MCP servers: list
// and read the servers' resources, and search the servers' deferred tool catalog.
// With the grant session as the one server (checkOnlyGrantServer), they reach the
// grant session and nothing else.
var mcpMediated = map[string]bool{
	"list_mcp_resources":          true,
	"list_mcp_resource_templates": true,
	"read_mcp_resource":           true,
	"tool_search":                 true,
}

// containerNamespaces group tools without being tools.
var containerNamespaces = map[string]bool{"functions": true}

// toolRef is one tool as the model would see it.
type toolRef struct {
	namespace string
	name      string
}

func (t toolRef) String() string {
	if t.namespace != "" {
		return t.namespace + "." + t.name
	}
	return t.name
}

// modelVisibleTools reads every tool a model request offers.
func modelVisibleTools(body map[string]any) []toolRef {
	var out []toolRef
	var walk func(ns string, tools []any)
	walk = func(ns string, tools []any) {
		for _, raw := range tools {
			t, ok := raw.(map[string]any)
			if !ok {
				out = append(out, toolRef{namespace: ns, name: "(unreadable tool)"})
				continue
			}
			typ, _ := t["type"].(string)
			name, _ := t["name"].(string)
			if inner, ok := t["tools"].([]any); ok || typ == "namespace" {
				walk(name, inner)
				continue
			}
			if name == "" {
				name = typ // tools such as web_search and tool_search are named by type
			}
			out = append(out, toolRef{namespace: ns, name: name})
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		walk("", tools)
	}
	if input, ok := body["input"].([]any); ok {
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			if tools, ok := item["tools"].([]any); ok {
				walk("", tools)
			}
		}
	}
	return out
}

// grantToolNames reads the grant session's tools from mcpServerStatus/list.
func grantToolNames(status map[string]any) []string {
	data, _ := status["data"].([]any)
	var names []string
	for _, d := range data {
		e, _ := d.(map[string]any)
		if n, _ := e["name"].(string); n != AttachmentName {
			continue
		}
		tools, _ := e["tools"].(map[string]any)
		for name := range tools {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// grantTool reports whether a model-visible tool is one of the grant session's,
// in any of the names Codex gives an MCP tool: mcp__<server>__<tool> flat, or the
// tool inside a namespace named for the server.
func grantTool(t toolRef, grant map[string]bool) bool {
	prefix := "mcp__" + AttachmentName + "__"
	if t.namespace == "" || containerNamespaces[t.namespace] {
		return strings.HasPrefix(t.name, prefix) && grant[strings.TrimPrefix(t.name, prefix)]
	}
	switch strings.TrimSuffix(t.namespace, "__") {
	case AttachmentName, "mcp__" + AttachmentName:
		return grant[t.name] || (strings.HasPrefix(t.name, prefix) && grant[strings.TrimPrefix(t.name, prefix)])
	}
	return false
}

// checkToolSurface fails closed unless every tool the model is offered is the
// grant session's or one of Codex's MCP-only helpers, and names each other tool.
func checkToolSurface(visible []toolRef, grantTools []string) error {
	grant := map[string]bool{}
	for _, n := range grantTools {
		grant[n] = true
	}
	var extra []string
	for _, t := range visible {
		if grantTool(t, grant) {
			continue
		}
		if mcpMediated[t.name] && (t.namespace == "" || containerNamespaces[t.namespace]) {
			continue
		}
		extra = append(extra, fmt.Sprintf("%q", t.String()))
	}
	if len(extra) > 0 {
		return fmt.Errorf("the thread offers the model tools that are not the grant session's: %s", strings.Join(extra, ", "))
	}
	return nil
}

var errNoToolSurface = errors.New("codex sent no model request to the runner's model proxy, so its tools could not be checked")
