package handoffrunner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Native tools (TENG-3693 B11). The grant session governs what the agent may do
// through the MCP attachment. Every tool Codex brings of its own (a shell, file
// edits, image viewing, web search, browsers, sub-agents, plugins, hooks) would
// reach the machine around that attachment, so the runner turns each one off in
// three places, then proves the result before any input is fetched:
//
//  1. Process: app-server starts with each feature below disabled (-c flags, so
//     they hold for every thread the process opens).
//  2. Model catalog: some tools come from the model's own catalog entry rather than
//     a feature (apply_patch, code mode's exec, sub-agent tools, extra
//     experimental tools). The runner writes a catalog, from this Codex binary's
//     own bundled one, with those entries cleared, and points Codex at it.
//  3. Thread: the thread config repeats the feature switches and turns off web
//     search, image viewing and the user-input tool.
//
// Then the guard: Codex's model traffic goes through the runner's model proxy,
// which reads the tools in every model request. Before any input, a probe thread
// on the same process sends one fixed local request that the proxy answers itself,
// and the runner checks that the tools Codex would hand the model are only the
// grant session's (see checkToolSurface). After that, every real request is
// checked the same way and refused, before it leaves the machine, if a tool
// appears that is not the grant session's.

// disabledFeatures are the Codex feature flags (codex features list, 0.161.0)
// that each add a model-visible tool or run something on the machine. Unknown
// names are accepted by Codex, so a flag a later version drops is harmless; a tool
// a later version adds under a new flag is caught by the guard instead.
var disabledFeatures = []string{
	"shell_tool", "unified_exec", "shell_snapshot", // the shell
	"view_image", "image_generation", // local image reads and generation
	"apps", "plugins", "remote_plugin", "tool_suggest", // built-in MCP servers and plugin tools
	"skill_search", "skill_mcp_dependency_install", "workspace_dependencies",
	"browser_use", "browser_use_external", "in_app_browser", "computer_use", // browser state and the desktop
	"multi_agent", "multi_agent_v2", // sub-agents with their own tools
	"memories", "goals", "sleep_tool", "hooks", "worktrees", "realtime_conversation",
	"code_mode", "code_mode_only", "code_mode_host",
	// Not a tool: compressed request bodies could not be read by the guard.
	"enable_request_compression",
}

// catalogFile is the tool-free model catalog the runner writes in Codex's home.
const catalogFile = "telara-model-catalog.json"

// appServerArgs are the arguments app-server starts with: the feature switches
// and the tool-free catalog. They are command-line configuration for this one
// process; nothing is written to the home's config.toml, which must not exist.
func appServerArgs(catalogPath string) []string {
	args := []string{"app-server"}
	for _, f := range disabledFeatures {
		args = append(args, "--disable", f)
	}
	if catalogPath != "" {
		b, _ := json.Marshal(catalogPath) // a TOML basic string is a JSON string
		args = append(args, "-c", "model_catalog_json="+string(b))
	}
	return args
}

// threadToolConfig is the part of the thread config that turns native tools off.
func threadToolConfig() map[string]any {
	features := map[string]any{}
	for _, f := range disabledFeatures {
		features[f] = false
	}
	return map[string]any{
		"features":   features,
		"web_search": "disabled",
		"tools": map[string]any{
			"web_search":                      false,
			"view_image":                      false,
			"experimental_request_user_input": map[string]any{"enabled": false},
		},
	}
}

// neutralizeCatalog clears, for every model, the catalog entries that add a tool
// of the model's own: apply_patch, code mode (which wraps tools in a JavaScript
// exec tool), the sub-agent tools and the model's extra experimental tools.
func neutralizeCatalog(raw []byte) ([]byte, error) {
	var catalog map[string]any
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("codex model catalog: %w", err)
	}
	models, ok := catalog["models"].([]any)
	if !ok || len(models) == 0 {
		return nil, errors.New("codex model catalog has no models")
	}
	for _, m := range models {
		model, ok := m.(map[string]any)
		if !ok {
			return nil, errors.New("codex model catalog has an entry that is not a model")
		}
		model["apply_patch_tool_type"] = nil
		model["tool_mode"] = nil
		model["multi_agent_version"] = nil
		model["experimental_supported_tools"] = []any{}
	}
	return json.Marshal(catalog)
}

// writeToolFreeCatalog asks this Codex binary for its own bundled catalog, offline
// and with the runner's environment only, and writes the neutralized copy into the
// home. It returns the file's path.
func writeToolFreeCatalog(bin, home string) (string, error) {
	cmd := exec.Command(bin, "debug", "models", "--bundled")
	cmd.Env = childEnv(home, "")
	cmd.Dir = home
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("codex debug models: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("codex debug models: %v: %s", err, bytes.TrimSpace(errb.Bytes()))
		}
	case <-time.After(callTimeout):
		_ = cmd.Process.Kill()
		return "", errors.New("codex debug models did not answer")
	}
	b, err := neutralizeCatalog(out.Bytes())
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, catalogFile)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// childEnv is the whole environment a Codex process gets: a few system variables
// and its home. tmp, when set, replaces TMPDIR with a directory inside the
// confinement, so temporary files stay where the process may write.
func childEnv(home, tmp string) []string {
	var env []string
	for _, key := range []string{"PATH", "LANG", "TMPDIR", "SYSTEMROOT", "WINDIR"} {
		v, ok := os.LookupEnv(key)
		if key == "TMPDIR" && tmp != "" {
			v, ok = tmp, true
		}
		if ok {
			env = append(env, key+"="+v)
		}
	}
	// Codex reads its home from CODEX_HOME. This points it at the runner's own
	// isolated home; it is Codex's variable, set for the child process only.
	return append(env, "CODEX_HOME="+home, "HOME="+home)
}
