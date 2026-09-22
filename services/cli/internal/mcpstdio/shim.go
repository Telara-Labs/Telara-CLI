// Package mcpstdio is the local MCP shim that gives one chat its own session
// (TENG-3019).
//
// WHY THIS EXISTS
//
// MCP revision 2026-07-28 removed `initialize` and `Mcp-Session-Id`, so the
// gateway issues no session id at all — internal/mcp/modern.go states it
// outright: "The server never issues a handle." A client configured against the
// HTTP endpoint therefore has no per-chat identity, and every window, subagent
// and CI run on one API key shares the single strand
// `stateless:<tenant>:<user>:<config>`. Observability Sessions cannot tell two
// chats apart and falls back to guessing a boundary from a 30-minute silence.
//
// The identity is not missing, only unowned: a conversation handle produces
// `conv:<tenant>:<user>:<config>:<handle>`, and everything downstream keys off it
// correctly. Nothing sends one, and nothing can over HTTP — a client's static
// config headers cannot vary per chat.
//
// A process can. One chat is one MCP server process, so the process boundary IS
// the conversation boundary: this shim mints a handle at startup and the chat it
// serves is identified for its whole life. That is why the shim is the fix and
// not a workaround — an idle gap is a guess about a fact the client knows for
// certain, and only something running inside the client can observe it.
//
// WHAT IT DOES NOT DO
//
// It does not forward `initialize`. An initialize POST is what selects the
// gateway's LEGACY path, which mints a uuid session and ignores the conversation
// handle (mcp-gateway streamable.go dispatches on the Mcp-Session-Id header,
// and reads the handle only in the stateless branch). The shim answers
// initialize itself and forwards everything after it with no session header, so
// those requests land in the stateless branch where the handle is read.
//
// It does not reshape frames. The handle travels as an HTTP header, the carrier
// the gateway offers "for SDKs that cannot shape _meta", so a client frame is
// forwarded byte-for-byte. Re-marshalling JSON reorders keys and rewrites number
// formatting: the frame would stay semantically equal while ceasing to be
// identical, and anything hashing or signing the raw bytes would break.
//
// It does not stream. POST /mcp answers application/json; the only SSE endpoint
// is GET /mcp, which requires a session header a stateless caller does not have.
// No server-initiated elicitation is pushed to these clients either — the
// gateway routes their approvals to the durable Telara approval path instead
// (internal/mcp/elicitation.go canPushElicitation). So approvals do not travel
// this path and are unaffected by it.
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HeaderConversation carries the conversation handle to the gateway. Mirrors
// mcp-gateway internal/mcp/modern.go HeaderConversation.
const HeaderConversation = "X-Telara-Conversation"

// maxFrameBytes bounds one JSON-RPC frame read from stdin. Tool arguments and
// results are routinely large; the default bufio.Scanner limit of 64KB truncates
// them mid-frame, which reads downstream as a parse error rather than as a size
// problem.
const maxFrameBytes = 32 << 20

// protocolVersion is what the shim advertises when the client does not name one.
// The shim speaks the transport the gateway's stateless branch serves.
const protocolVersion = "2025-06-18"

// Shim relays one client's stdio MCP session to the gateway over HTTP.
type Shim struct {
	// Endpoint is the gateway's streamable MCP URL (no /sse suffix).
	Endpoint string
	// APIKey is the raw MCP API key sent as a bearer token.
	APIKey string
	// Conversation identifies this chat for the gateway. One per process.
	Conversation string
	// HTTP is the client used for forwarding. Nil means http.DefaultClient.
	HTTP *http.Client
	// ServerName is reported to the client in the initialize result.
	ServerName string
	// ServerVersion is reported to the client in the initialize result.
	ServerVersion string
}

// New returns a Shim with a freshly minted conversation handle.
//
// The handle is minted per process and never reused: two chats are two
// processes, so two handles, so two sessions. A handle that outlived the process
// would merge the next chat into this one.
func New(endpoint, apiKey, serverVersion string) (*Shim, error) {
	handle, err := NewConversationHandle()
	if err != nil {
		return nil, err
	}
	return &Shim{
		Endpoint:      endpoint,
		APIKey:        apiKey,
		Conversation:  handle,
		ServerName:    "telara",
		ServerVersion: serverVersion,
	}, nil
}

// NewConversationHandle mints a handle for one chat.
//
// 128 bits of crypto/rand as hex: within the gateway's accepted shape of 1..128
// characters of [A-Za-z0-9._:-], and wide enough that two concurrent chats
// cannot collide onto one session row. A failure to read randomness is returned
// rather than papered over with a time-based value, which would collide across
// processes started in the same instant — exactly the case this exists to tell
// apart.
func NewConversationHandle() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mint conversation handle: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// envelope is the part of a JSON-RPC frame the shim reads. Everything else is
// forwarded untouched, so nothing else is decoded.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports a frame the client expects no reply to. JSON-RPC
// notifications carry no id; answering one is a protocol violation.
func (e envelope) isNotification() bool {
	return len(e.ID) == 0 || string(e.ID) == "null"
}

// Run reads newline-delimited JSON-RPC frames from in and writes replies to out
// until in is exhausted.
//
// Frames are handled one at a time. MCP stdio is an ordered stream and a client
// may rely on that order; concurrency here would reorder replies for no gain,
// since a chat issues one tool call at a time.
func (s *Shim) Run(ctx context.Context, in io.Reader, out io.Writer, errOut io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), maxFrameBytes)

	writer := bufio.NewWriter(out)
	defer writer.Flush()

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// The scanner reuses its buffer, and the frame is forwarded after the
		// next read may have overwritten it.
		frame := append([]byte(nil), line...)

		reply, err := s.handle(ctx, frame)
		if err != nil {
			fmt.Fprintf(errOut, "telara mcp: %v\n", err)
		}
		if reply == nil {
			continue
		}
		if _, err := writer.Write(append(reply, '\n')); err != nil {
			return fmt.Errorf("write reply: %w", err)
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("flush reply: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

// handle returns the frame to write back, or nil when the client expects none.
//
// A transport failure becomes a JSON-RPC error reply rather than a dropped
// frame: a client waiting on an id it never gets back hangs, where an error is
// something it can report.
func (s *Shim) handle(ctx context.Context, frame []byte) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		// Unparseable input cannot be answered — the id is part of what failed
		// to parse, and a reply with no id is not addressed to anything.
		return nil, fmt.Errorf("malformed frame from client: %w", err)
	}

	// initialize is answered here, never forwarded: forwarding it would select
	// the gateway's legacy session path and discard the conversation handle.
	if env.Method == "initialize" {
		if env.isNotification() {
			return nil, nil
		}
		return s.initializeResult(env)
	}

	status, body, err := s.forward(ctx, frame)
	if env.isNotification() {
		// Notifications get no reply whatever happened, but a failure is still
		// worth surfacing on stderr rather than swallowing.
		return nil, err
	}
	if err != nil {
		return errorReply(env.ID, -32603, err.Error())
	}
	if status >= 400 {
		// The gateway answers errors as JSON-RPC too; pass its own wording
		// through when it did, so the client sees the real reason.
		if json.Valid(body) && bytes.Contains(body, []byte(`"jsonrpc"`)) {
			return body, nil
		}
		return errorReply(env.ID, -32603, fmt.Sprintf("gateway returned HTTP %d: %s", status, truncate(string(body), 300)))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errorReply(env.ID, -32603, "gateway returned an empty response")
	}
	return body, nil
}

// forward POSTs one client frame to the gateway, verbatim.
func (s *Shim) forward(ctx context.Context, frame []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(frame))
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	// The whole point of the shim. Same value on every request of this process,
	// different between processes.
	req.Header.Set(HeaderConversation, s.Conversation)
	// Deliberately NOT set: Mcp-Session-Id. Sending one would take the gateway's
	// legacy path, where the conversation handle is not read.

	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach gateway: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read gateway response: %w", err)
	}
	return resp.StatusCode, bytes.TrimSpace(body), nil
}

// initializeResult answers the client's handshake locally.
//
// The version is echoed back when the client names one the shim can speak, so a
// client is never told it got a revision it did not ask for. Capabilities claim
// only tools: the shim adds none of its own, and the gateway's stateless branch
// serves tools.
func (s *Shim) initializeResult(env envelope) ([]byte, error) {
	requested := struct {
		ProtocolVersion string `json:"protocolVersion"`
	}{}
	if len(env.Params) > 0 {
		_ = json.Unmarshal(env.Params, &requested)
	}
	version := strings.TrimSpace(requested.ProtocolVersion)
	if version == "" {
		version = protocolVersion
	}

	result := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    s.ServerName,
			"version": s.ServerVersion,
		},
	}
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(env.ID),
		"result":  result,
	})
}

func errorReply(id json.RawMessage, code int, message string) ([]byte, error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// DefaultHTTPClient is the forwarding client used by the command.
//
// No overall timeout: a tool call can legitimately run for minutes, and a
// deadline here would cancel real work. The per-connection timeouts below still
// fail a dead endpoint quickly.
func DefaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transport}
}
