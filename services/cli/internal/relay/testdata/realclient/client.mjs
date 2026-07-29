// A real MCP client, built on the official @modelcontextprotocol/sdk, used to
// drive the TENG-1528 relay end to end.
//
// This exists because the rest of the spike's client side is synthetic JSON-RPC
// written by the same author as the relay. That can only prove the relay is
// self-consistent. Stop rule #1 is about real clients, so the compatibility
// claim needs a client implementation Telara did not write — one that performs
// its own capability negotiation, schema validation and framing.
//
// It launches the relay binary exactly as it would launch a server. The relay
// resolves the real server from RELAY_SPIKE_SERVER.
//
// Emits a single JSON line on stdout so the Go test can assert on it.

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";

const relayBin = process.env.RELAY_BIN;
const serverCmd = process.env.RELAY_SPIKE_SERVER;
const observations = process.env.RELAY_SPIKE_OBSERVATIONS ?? "";
const toolName = process.env.PROBE_TOOL ?? "echo";
const toolArgs = JSON.parse(process.env.PROBE_TOOL_ARGS ?? "{}");

if (!relayBin || !serverCmd) {
  console.error("RELAY_BIN and RELAY_SPIKE_SERVER are required");
  process.exit(2);
}

const transport = new StdioClientTransport({
  command: relayBin,
  args: [],
  env: {
    ...process.env,
    RELAY_SPIKE_SERVER: serverCmd,
    RELAY_SPIKE_OBSERVATIONS: observations,
  },
  stderr: "inherit",
});

const client = new Client(
  { name: "relay-spike-real-client", version: "1.0.0" },
  { capabilities: {} },
);

const result = { ok: false };
try {
  // connect() performs the real initialize handshake and version negotiation.
  await client.connect(transport);

  const serverInfo = client.getServerVersion();
  result.serverName = serverInfo?.name ?? null;
  result.serverVersion = serverInfo?.version ?? null;

  const tools = await client.listTools();
  result.toolCount = tools.tools.length;
  result.toolNames = tools.tools.map((t) => t.name).slice(0, 40);

  // A real tool call, with the SDK validating the response shape.
  const called = await client.callTool({ name: toolName, arguments: toolArgs });
  result.calledTool = toolName;
  result.callIsError = called.isError === true;
  result.callTextLength = (called.content ?? [])
    .filter((c) => c.type === "text")
    .reduce((n, c) => n + c.text.length, 0);
  result.callText = (called.content ?? [])
    .filter((c) => c.type === "text")
    .map((c) => c.text)
    .join("")
    .slice(0, 200);

  result.ok = true;
} catch (err) {
  result.error = String(err && err.stack ? err.stack : err).slice(0, 800);
} finally {
  try {
    await client.close();
  } catch {
    // The session is already finished; a close error must not mask the result.
  }
}

process.stdout.write(JSON.stringify(result) + "\n");
process.exit(result.ok ? 0 : 1);
