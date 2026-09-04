package agent

import "testing"

// PlatformToolNames is a copy of a list the MCP gateway owns. The live list is
// fetched at setup time, so this only matters when the server is unreachable —
// but a stale copy silently re-prompts the user for anything missing, which is
// exactly the bug this replaced. Pinned so a gateway change that reaches this
// repo has to update it deliberately (TENG-2223).
func TestPlatformToolNamesMatchesGatewaySurface(t *testing.T) {
	names := map[string]bool{}
	for _, n := range PlatformToolNames() {
		if names[n] {
			t.Errorf("duplicate entry %q", n)
		}
		names[n] = true
	}

	for _, required := range []string{
		"telara_knowledge_search", "telara_knowledge_traverse", "telara_knowledge_get_context",
		"telara_knowledge_impact", "telara_knowledge_annotate", "telara_knowledge_link",
		"telara_knowledge_timeline", "telara_code_call_hierarchy", "telara_browser_extract",
		"telara_archive_read", "telara_archive_ls", "telara_archive_search",
		"telara_task_list", "telara_task_create", "telara_task_resume",
		"telara_task_checkpoint", "telara_task_complete", "telara_task_pause",
		"telara_execute_action", "telara_tool_search", "telara_tool_describe",
		// Shared skills (TENG-2719). Present here for the same reason as the
		// rest: Cursor, Windsurf and Codex auto-approve by explicit NAME, so a
		// tool missing from this fallback re-prompts the user on every call
		// whenever the live gateway list cannot be fetched.
		"telara_skill_search", "telara_skill_load", "telara_skill_publish",
	} {
		if !names[required] {
			t.Errorf("fallback is missing %q — Cursor and Windsurf will re-prompt for it", required)
		}
	}

	// Retired from the advertised surface; auto-approving it is harmless but
	// listing it here would mean the copy had not been updated.
	if names["telara_archive_batch_read"] {
		t.Error("telara_archive_batch_read is no longer advertised; it folded into archive_read")
	}

	// 21 -> 24 (TENG-2719). TENG-2309 called the 21 a "permanent public
	// connector tool contract"; that promise is that a published name keeps
	// working and keeps meaning what it meant — clients pin these names — not
	// that the set is frozen. Removing or repurposing one would break it;
	// adding three that no existing client references does not.
	//
	// The count is asserted rather than just the membership above so that a
	// tool added to the gateway without being added here is caught: that is
	// the drift TestPlatformToolNamesMatchesGatewaySurface exists to prevent.
	if len(names) != 24 {
		t.Errorf("expected the 24-tool core, got %d: %v", len(names), PlatformToolNames())
	}
}
