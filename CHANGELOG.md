# Changelog

## Unreleased

- Fixed `telara handoff runner` cancelling every run on Codex accounts that add a built-in `codex_apps` MCP server. The runner now turns Codex apps off for its thread, and still refuses any other MCP server besides the grant session, naming it.
- Added `telara handoff runner` (`setup`, `login`, `run`). It starts your own Codex, on your machine, for handoffs you accepted for it: one fresh thread per run whose only tool connection is that work's grant, in an empty working directory with no network, signed in to Codex inside the runner's own home. It stops when Telara says stop or the grant ends, and never stores the session credential.
- The handoff runner now has its own key pair, made by `telara handoff runner setup` and kept in the runner's directory readable by you only. Each claim proves the key and the run's session is bound to it, so a copy of the session credential is refused anywhere else. Codex reaches the session through a per-run local proxy that adds the credential and a key proof to every call, so Codex never holds either.
- Added `telara_handoff_open` and `telara_handoff_request` to the agent permission list, so a governed handoff can run without a prompt for each tool.
- Fixed `telara logout` revoking the wrong MCP key. Key prefixes are shared by every key of a tenant, so logout now revokes the key by the id `telara install` caches with it (`mcp_key_id`). A key cached before this change is revoked only when exactly one live key matches its prefix, otherwise logout fails and names the candidate keys.
- Fixed the "the token is still valid on the server" statement being replaced by the generic "Could not reach the Telara API" line when the server was unreachable during `telara logout`.
