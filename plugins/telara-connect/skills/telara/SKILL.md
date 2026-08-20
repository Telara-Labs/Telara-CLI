---
name: telara
description: Use Telara to securely search authorized company systems, investigate connected knowledge, and perform approved work on behalf of the signed-in employee.
---

# Telara

Use Telara when the user needs information or an action in company systems that
their organization has connected and approved. Telara applies the signed-in
employee's existing organization access and action policies on every request.

## Start with the user's goal

- Use `telara_knowledge_search` for a focused search across authorized company
  knowledge.
- Use `telara_traverse`, `telara_get_context`, `telara_timeline`,
  `telara_impact`, `telara_link`, or `telara_annotate` when the request needs
  connected evidence, history, impact, relationships, or annotations.
- Use `telara_code_call_hierarchy` and `telara_browser_extract` for authorized
  code-relationship and webpage-extraction work.
- Use `telara_archive_read`, `telara_archive_ls`, or `telara_archive_search`
  for archived material.
- Use the `telara_task_*` tools to list, create, resume, checkpoint, pause, or
  complete an authorized Telara task.

## Perform work safely

1. Before an external action, call `telara://integrations/available` and use
   `telara_tool_search` or `telara_tool_describe` when you need the employee's
   currently allowed actions and parameter shapes.
2. Explain the intended result in plain language. Do not claim an action was
   completed until Telara returns a successful result.
3. Call `telara_execute_action` only with the discovered integration, action,
   and parameters. Telara enforces the employee and organization policy at
   execution time.
4. If Telara requires an approval or rejects an action, report that outcome and
   do not retry with altered parameters to bypass policy.

## Connection and privacy

- Do not ask the user for an API key, MCP URL, tenant ID, access token, or
  copied credentials. The Telara connection is established with employee OAuth.
- Do not infer access from a successful search. Permissions can differ by
  system and action, and Telara is the authority for each request.
- Keep results scoped to the user's request. Do not use Telara to collect or
  disclose data beyond what is needed to complete that request.

## Tool contract

The public connector provides these 21 tools:

`telara_knowledge_search`, `telara_traverse`, `telara_get_context`,
`telara_impact`, `telara_annotate`, `telara_link`, `telara_timeline`,
`telara_code_call_hierarchy`, `telara_browser_extract`,
`telara_archive_read`, `telara_archive_ls`, `telara_archive_search`,
`telara_task_list`, `telara_task_create`, `telara_task_resume`,
`telara_task_checkpoint`, `telara_task_complete`, `telara_task_pause`,
`telara_execute_action`, `telara_tool_search`, and `telara_tool_describe`.

`telara_execute_action` is the canonical action tool. Do not use deprecated or
invented action-tool names.
