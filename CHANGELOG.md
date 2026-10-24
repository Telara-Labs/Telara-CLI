# Changelog

## Unreleased

- Fixed `telara logout` revoking the wrong MCP key. Key prefixes are shared by every key of a tenant, so logout now revokes the key by the id `telara install` caches with it (`mcp_key_id`). A key cached before this change is revoked only when exactly one live key matches its prefix, otherwise logout fails and names the candidate keys.
- Fixed the "the token is still valid on the server" statement being replaced by the generic "Could not reach the Telara API" line when the server was unreachable during `telara logout`.
