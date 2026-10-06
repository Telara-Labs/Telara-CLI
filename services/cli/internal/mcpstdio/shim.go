// Package mcpstdio is the local MCP shim that gives one chat its own session
// (TENG-3019).
//
// # WHY THIS EXISTS
//
// MCP revision 2026-07-28 removed `initialize` and `Mcp-Session-Id`, so the
// gateway issues no session id at all — internal/mcp/modern.go states it
// outright: "The server never issues a handle." A client configured against the
// HTTP endpoint therefore has no per-chat identity, and every window, subagent
// and CI run on one API key shares the single strand
// `stateless:<tenant>:<user>:<config>`. Observability Sessions cannot tell two
// chats apart and falls back to guessing a boundary from a 30-minute silence.
//
// A conversation handle produces `conv:<tenant>:<user>:<config>:<handle>`, and
// everything downstream keys off it correctly. A client's static HTTP config
// cannot vary that header per chat; this per-process shim can.
//
// A process can. One chat is one MCP server process, so the process boundary IS
// the conversation boundary: this shim mints a handle at startup and the chat it
// serves is identified for its whole life. That is why the shim is the fix and
// not a workaround — an idle gap is a guess about a fact the client knows for
// certain, and only something running inside the client can observe it.
//
// # WHAT IT DOES NOT DO
//
// It does not reshape frames. The handle travels as an HTTP header, the carrier
// the gateway offers "for SDKs that cannot shape _meta", so a client frame is
// forwarded byte-for-byte. Re-marshalling JSON reorders keys and rewrites number
// formatting: the frame would stay semantically equal while ceasing to be
// identical, and anything hashing or signing the raw bytes would break.
//
// It relays server-initiated JSON-RPC requests (including approval elicitation)
// over the stdio stream while the upstream Streamable HTTP request is open.
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HeaderConversation carries the process-scoped conversation handle to the
// gateway. Stateless requests use it directly; initialized sessions also carry
// the gateway's MCP session ID so elicitation can be relayed over stdio.
const HeaderConversation = "X-Telara-Conversation"

// maxFrameBytes bounds one JSON-RPC frame read from stdin. Tool arguments and
// results are routinely large; the default bufio.Scanner limit of 64KB truncates
// them mid-frame, which reads downstream as a parse error rather than as a size
// problem.
const maxFrameBytes = 32 << 20

// elicitationResponseTimeout must exceed mcp-gateway's 90-second ElicitTimeout.
// It lets the gateway return its ordinary approval-link fallback if a host
// advertises elicitation but never surfaces the request to its user.
const elicitationResponseTimeout = 95 * time.Second

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
	// SessionID and ProtocolVersion are negotiated from the gateway's forwarded
	// initialize response and attached to later Streamable HTTP requests.
	SessionID       string
	ProtocolVersion string
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
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// isNotification reports a frame the client expects no reply to. JSON-RPC
// notifications carry no id; answering one is a protocol violation.
func (e envelope) isNotification() bool {
	return len(e.ID) == 0 || string(e.ID) == "null"
}

func (e envelope) isJSONRPCResponse() bool {
	return e.Method == "" && !e.isNotification() && (len(e.Result) > 0 || len(e.Error) > 0)
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
	frames := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			frame := append([]byte(nil), line...)
			select {
			case frames <- frame:
			case <-ctx.Done():
				readErr <- ctx.Err()
				close(frames)
				return
			}
		}
		readErr <- scanner.Err()
		close(frames)
	}()

	writer := bufio.NewWriter(out)
	defer writer.Flush()
	bridge := &stdioBridge{frames: frames, readErr: readErr, writer: writer}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := bridge.nextFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read stdin: %w", err)
		}

		reply, err := s.handle(ctx, frame, bridge)
		if err != nil {
			fmt.Fprintf(errOut, "telara mcp: %v\n", err)
		}
		if reply == nil {
			continue
		}
		if err := bridge.send(reply); err != nil {
			return fmt.Errorf("write reply: %w", err)
		}
	}
}

type stdioBridge struct {
	frames   <-chan []byte
	readErr  <-chan error
	writer   *bufio.Writer
	deferred [][]byte
}

func (b *stdioBridge) nextFrame() ([]byte, error) {
	for len(b.deferred) > 0 {
		frame := b.deferred[0]
		b.deferred = b.deferred[1:]
		return frame, nil
	}
	frame, ok := <-b.frames
	if !ok {
		if err := <-b.readErr; err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return frame, nil
}

func (b *stdioBridge) send(frame []byte) error {
	if _, err := b.writer.Write(append(frame, '\n')); err != nil {
		return err
	}
	return b.writer.Flush()
}

func (b *stdioBridge) waitForResponse(ctx context.Context, serverRequestID, clientRequestID json.RawMessage, cancel context.CancelFunc, timeout time.Duration) ([]byte, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		var frame []byte
		var ok bool
		select {
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		case <-timer.C:
			// Do not cancel the HTTP request: mcp-gateway will hit its own
			// elicitation deadline and return the durable approval-link fallback.
			return nil, errElicitationResponseTimeout
		case frame, ok = <-b.frames:
			if !ok {
				if err := <-b.readErr; err != nil {
					cancel()
					return nil, err
				}
				cancel()
				return nil, io.EOF
			}
		}
		var incoming envelope
		if json.Unmarshal(frame, &incoming) != nil {
			b.deferred = append(b.deferred, frame)
			continue
		}
		if incoming.isJSONRPCResponse() && bytes.Equal(incoming.ID, serverRequestID) {
			return frame, nil
		}
		if incoming.Method == "notifications/cancelled" && cancellationMatches(incoming.Params, clientRequestID) {
			cancel()
			return nil, context.Canceled
		}
		// Preserve unrelated frames for normal dispatch after the active call.
		b.deferred = append(b.deferred, frame)
	}
}

var errElicitationResponseTimeout = errors.New("stdio client did not answer the elicitation before the gateway fallback deadline")

func cancellationMatches(params, requestID json.RawMessage) bool {
	var value struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	return len(requestID) > 0 && json.Unmarshal(params, &value) == nil && bytes.Equal(value.RequestID, requestID)
}

// handle returns the frame to write back, or nil when the client expects none.
//
// A transport failure becomes a JSON-RPC error reply rather than a dropped
// frame: a client waiting on an id it never gets back hangs, where an error is
// something it can report.
func (s *Shim) handle(ctx context.Context, frame []byte, bridge *stdioBridge) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		// Unparseable input cannot be answered — the id is part of what failed
		// to parse, and a reply with no id is not addressed to anything.
		return nil, fmt.Errorf("malformed frame from client: %w", err)
	}

	status, body, err := s.forward(ctx, frame, env, bridge)
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
func (s *Shim) forward(ctx context.Context, frame []byte, env envelope, bridge *stdioBridge) (int, []byte, error) {
	requestCtx := ctx
	cancel := func() {}
	if env.Method == "tools/call" {
		requestCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.Endpoint, bytes.NewReader(frame))
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	// The whole point of the shim. Same value on every request of this process,
	// different between processes.
	req.Header.Set(HeaderConversation, s.Conversation)
	if s.SessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.SessionID)
	}
	if env.Method != "initialize" && s.ProtocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", s.ProtocolVersion)
	}

	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach gateway: %w", err)
	}
	defer resp.Body.Close()
	if env.Method == "initialize" && resp.StatusCode < 400 {
		s.SessionID = strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
		if s.SessionID == "" {
			return resp.StatusCode, nil, errors.New("gateway initialize response omitted Mcp-Session-Id")
		}
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		body, err := s.readEventStream(requestCtx, resp.Body, env.ID, bridge, cancel, elicitationResponseTimeout)
		return resp.StatusCode, body, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read gateway response: %w", err)
	}
	body = bytes.TrimSpace(body)
	if env.Method == "initialize" && resp.StatusCode < 400 {
		body, err = s.decorateInitializeResponse(body)
		if err != nil {
			return resp.StatusCode, nil, err
		}
		var result struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return resp.StatusCode, body, fmt.Errorf("decode gateway initialize response: %w", err)
		}
		s.ProtocolVersion = result.Result.ProtocolVersion
	}
	return resp.StatusCode, body, nil
}

func (s *Shim) decorateInitializeResponse(body []byte) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode gateway initialize response: %w", err)
	}
	result, _ := response["result"].(map[string]any)
	if result == nil {
		return body, nil
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo == nil {
		serverInfo = make(map[string]any)
		result["serverInfo"] = serverInfo
	}
	if s.ServerName != "" {
		serverInfo["name"] = s.ServerName
	}
	if s.ServerVersion != "" {
		serverInfo["version"] = s.ServerVersion
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode shim initialize response: %w", err)
	}
	return encoded, nil
}

func (s *Shim) readEventStream(ctx context.Context, body io.Reader, clientRequestID json.RawMessage, bridge *stdioBridge, cancel context.CancelFunc, responseTimeout time.Duration) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxFrameBytes)
	var dataLines []string
	processEvent := func() ([]byte, bool, error) {
		if len(dataLines) == 0 {
			return nil, false, nil
		}
		data := []byte(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		var message envelope
		if err := json.Unmarshal(data, &message); err != nil {
			return nil, false, fmt.Errorf("decode gateway SSE message: %w", err)
		}
		if message.Method != "" {
			if err := bridge.send(data); err != nil {
				return nil, false, fmt.Errorf("write gateway request to stdio client: %w", err)
			}
			if !message.isNotification() {
				response, err := bridge.waitForResponse(ctx, message.ID, clientRequestID, cancel, responseTimeout)
				if err != nil {
					if errors.Is(err, errElicitationResponseTimeout) {
						// Keep reading the gateway stream; it will return the
						// approval-specific link after its own decision timeout.
						return nil, false, nil
					}
					return nil, false, fmt.Errorf("wait for stdio client response: %w", err)
				}
				if err := s.forwardServerResponse(ctx, response); err != nil {
					return nil, false, err
				}
			}
			return nil, false, nil
		}
		if message.isJSONRPCResponse() {
			if !bytes.Equal(message.ID, clientRequestID) {
				return nil, false, fmt.Errorf("gateway SSE response id %s does not match client request id %s", message.ID, clientRequestID)
			}
			return data, true, nil
		}
		// Server notifications (for example, progress) are part of the MCP
		// stream and belong on stdout even though they have no matching request.
		if err := bridge.send(data); err != nil {
			return nil, false, fmt.Errorf("write gateway notification to stdio client: %w", err)
		}
		return nil, false, nil
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()
		if line == "" {
			if result, done, err := processEvent(); err != nil || done {
				return result, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") { // SSE keepalive comment.
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field, value = line, ""
		}
		if field == "data" {
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			dataLines = append(dataLines, value)
			if len(strings.Join(dataLines, "\n")) > maxFrameBytes {
				return nil, fmt.Errorf("gateway SSE event exceeds %d bytes", maxFrameBytes)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read gateway SSE: %w", err)
	}
	if result, done, err := processEvent(); err != nil || done {
		return result, err
	}
	return nil, io.ErrUnexpectedEOF
}

func (s *Shim) forwardServerResponse(ctx context.Context, frame []byte) error {
	if s.SessionID == "" {
		return errors.New("cannot forward elicitation response without an MCP session")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(frame))
	if err != nil {
		return fmt.Errorf("build elicitation response: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set(HeaderConversation, s.Conversation)
	req.Header.Set("Mcp-Session-Id", s.SessionID)
	if s.ProtocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", s.ProtocolVersion)
	}
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("forward elicitation response: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gateway rejected elicitation response with HTTP %d", resp.StatusCode)
	}
	return nil
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
