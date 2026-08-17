# Connect Telara

This package declares Telara's universal remote MCP endpoint without embedding
an API key or asking a user to edit an MCP file. A supported client discovers
Telara's OAuth endpoints, registers itself, and opens Telara's login and consent
screen. Telara binds the resulting authorization to the signed-in employee and
their existing organization membership; the client does not send a tenant ID or
choose a tenant-specific endpoint.

The plugin deliberately contains only Telara's remote MCP endpoint. It contains
no API key, no bearer token, and no per-user configuration. Telara identifies
the signed-in user and tenant through OAuth; removing the OAuth connection in
Claude Code or revoking the client/user in Telara stops future access.

For governed operations, the connector advertises the canonical generic action
tool `telara_execute_action`. Clients first read `telara://integrations/available`
to discover the employee's allowed integrations, actions, and parameter shapes,
then call `telara_execute_action` with `{ integration, action, params }`.
Telara evaluates the employee and tenant policy at execution time; the connector
does not grant arbitrary or tenant-wide access.

For Claude's public Connectors Directory, submit the existing endpoint directly
as a remote connector. The same package also supplies Claude Code and Cursor
marketplace metadata for managed or one-click distribution without provisioning
a shared Telara credential. In every case each employee completes their own
Telara OAuth consent, and removing the connection or revoking the employee or
OAuth client stops future access.
