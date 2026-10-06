package mcpstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture is a stand-in gateway: it records what the shim actually sent.
type capture struct {
	mu       sync.Mutex
	bodies   []string
	headers  []http.Header
	status   int
	response string
	server   *httptest.Server
}

func newCapture(t *testing.T) *capture {
	t.Helper()
	c := &capture{status: http.StatusOK, response: `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request envelope
		_ = json.Unmarshal(body, &request)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(body))
		c.headers = append(c.headers, r.Header.Clone())
		status, response := c.status, c.response
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-test")
			response = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"gateway-telara","version":"2.0"}}}`, request.ID)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *capture) seen() ([]string, []http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...), append([]http.Header(nil), c.headers...)
}

func run(t *testing.T, c *capture, input string) (stdout string, stderr string) {
	t.Helper()
	shim, err := New(c.server.URL, "raw-key", "1.2.3")
	require.NoError(t, err)
	shim.HTTP = c.server.Client()

	var out, errOut strings.Builder
	require.NoError(t, shim.Run(context.Background(), strings.NewReader(input), &out, &errOut))
	return out.String(), errOut.String()
}

func TestShim_ForwardsTheHandleOnEveryRequestAndKeepsItStableInOneProcess(t *testing.T) {
	c := newCapture(t)

	run(t, c, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"telara_jira_add_comment"}}`,
	}, "\n")+"\n")

	_, headers := c.seen()
	require.Len(t, headers, 2)
	handle := headers[0].Get(HeaderConversation)
	assert.NotEmpty(t, handle, "every forwarded request must name the conversation")
	assert.Equal(t, handle, headers[1].Get(HeaderConversation),
		"one process is one chat: the handle must not change between requests")
	assert.Equal(t, "Bearer raw-key", headers[0].Get("Authorization"))
}

func TestShim_NeverSendsASessionHeader(t *testing.T) {
	c := newCapture(t)

	// An Mcp-Session-Id would take the gateway's legacy path, where the
	// conversation handle is not read at all — silently undoing the whole point.
	run(t, c, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")

	_, headers := c.seen()
	require.Len(t, headers, 1)
	assert.Empty(t, headers[0].Get("Mcp-Session-Id"))
}

func TestShim_HandlesAreUniquePerProcess(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		handle, err := NewConversationHandle()
		require.NoError(t, err)
		assert.False(t, seen[handle], "two chats must never share a handle")
		seen[handle] = true
	}
}

func TestShim_HandleMatchesTheGatewaysAcceptedShape(t *testing.T) {
	// mcp-gateway ValidateConversationHandle: 1..128 chars of [A-Za-z0-9._:-].
	shape := regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	handle, err := NewConversationHandle()
	require.NoError(t, err)
	assert.Regexp(t, shape, handle)
}

func TestShim_ForwardsInitializeCapabilitiesAndUsesGatewaySession(t *testing.T) {
	c := newCapture(t)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"claude-code"},"capabilities":{"elicitation":{"form":{}}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"
	stdout, _ := run(t, c, input)

	bodies, headers := c.seen()
	require.Len(t, bodies, 2, "initialize and tools/list must both reach the gateway")
	assert.Contains(t, bodies[0], `"elicitation":{"form":{}}`, "the client's live capability must reach the gateway")
	assert.Empty(t, headers[0].Get("Mcp-Session-Id"), "initialize creates, rather than uses, the upstream session")
	assert.Equal(t, "session-test", headers[1].Get("Mcp-Session-Id"))
	assert.Equal(t, "2025-06-18", headers[1].Get("MCP-Protocol-Version"))

	var reply struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &reply))
	assert.Equal(t, 1, reply.ID)
	assert.Equal(t, "2025-06-18", reply.Result.ProtocolVersion, "the client's version is echoed, not overridden")
	assert.Equal(t, "telara", reply.Result.ServerInfo.Name)
	assert.Equal(t, "1.2.3", reply.Result.ServerInfo.Version)
	assert.Contains(t, reply.Result.Capabilities, "tools")
}

func TestShim_RelaysGatewayElicitationThroughStdioAndForwardsClientResponse(t *testing.T) {
	responsePosted := make(chan struct{})
	var responseBody string
	var conversation string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var request envelope
		require.NoError(t, json.Unmarshal(body, &request))
		switch request.Method {
		case "initialize":
			assert.Contains(t, string(body), `"elicitation":{"form":{}}`)
			mu.Lock()
			conversation = r.Header.Get(HeaderConversation)
			mu.Unlock()
			assert.NotEmpty(t, conversation)
			w.Header().Set("Mcp-Session-Id", "session-approval")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"gateway","version":"2"}}}`)
		case "notifications/initialized":
			assert.Equal(t, "session-approval", r.Header.Get("Mcp-Session-Id"))
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			assert.Equal(t, "session-approval", r.Header.Get("Mcp-Session-Id"))
			assert.Equal(t, "2025-06-18", r.Header.Get("MCP-Protocol-Version"))
			assert.Contains(t, r.Header.Get("Accept"), "text/event-stream")
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)
			_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"elicit-1\",\"method\":\"elicitation/create\",\"params\":{\"mode\":\"form\",\"message\":\"Approve Jira filter?\"}}\n\n")
			flusher.Flush()
			select {
			case <-responsePosted:
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
				t.Error("gateway did not receive elicitation response")
				return
			}
			_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Action denied\"}]}}\n\n")
			flusher.Flush()
		case "":
			require.Equal(t, "session-approval", r.Header.Get("Mcp-Session-Id"))
			require.Equal(t, "2025-06-18", r.Header.Get("MCP-Protocol-Version"))
			require.Equal(t, "Bearer raw-key", r.Header.Get("Authorization"))
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			require.Equal(t, "application/json", r.Header.Get("Accept"))
			mu.Lock()
			expectedConversation := conversation
			responseBody = string(body)
			mu.Unlock()
			require.NotEmpty(t, expectedConversation)
			require.Equal(t, expectedConversation, r.Header.Get(HeaderConversation))
			close(responsePosted)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected gateway method %q", request.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	shim, err := New(server.URL, "raw-key", "1.2.3")
	require.NoError(t, err)
	shim.HTTP = server.Client()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	errReader, errWriter := io.Pipe()
	runDone := make(chan error, 1)
	go func() { runDone <- shim.Run(context.Background(), inReader, outWriter, errWriter) }()
	clientOutput := bufio.NewReader(outReader)
	write := func(frame string) {
		t.Helper()
		_, err := io.WriteString(inWriter, frame+"\n")
		require.NoError(t, err)
	}
	read := func() string {
		t.Helper()
		line, err := clientOutput.ReadString('\n')
		require.NoError(t, err)
		return strings.TrimSpace(line)
	}

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test-host"},"capabilities":{"elicitation":{"form":{}}}}}`)
	assert.Contains(t, read(), `"protocolVersion":"2025-06-18"`)
	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	write(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"telara_jira_create_filter","arguments":{"name":"approval-test"}}}`)
	prompt := read()
	assert.Contains(t, prompt, `"method":"elicitation/create"`)
	assert.Contains(t, prompt, "Approve Jira filter?")
	write(`{"jsonrpc":"2.0","id":"elicit-1","result":{"action":"decline"}}`)
	final := read()
	assert.Contains(t, final, `"id":3`)
	assert.Contains(t, final, "Action denied")
	mu.Lock()
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"elicit-1","result":{"action":"decline"}}`, responseBody)
	mu.Unlock()

	require.NoError(t, inWriter.Close())
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("stdio shim did not stop after client input closed")
	}
	_ = errWriter.Close()
	_ = errReader.Close()
}

func TestShim_PreservesApprovalSpecificLinkFallback(t *testing.T) {
	c := newCapture(t)
	c.response = `{"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"Approval pending: https://app.telara.dev/approvals?approval_id=approval-42"}],"structuredContent":{"approval":{"approvals_url":"https://app.telara.dev/approvals?approval_id=approval-42"}}}}`

	stdout, _ := run(t, c, `{"jsonrpc":"2.0","id":5,"method":"tools/call"}`+"\n")

	assert.Contains(t, stdout, "https://app.telara.dev/approvals?approval_id=approval-42")
}

func TestShim_NoClientResponseContinuesReadingGatewayLinkFallback(t *testing.T) {
	shim, err := New("http://gateway.invalid/mcp", "raw-key", "1.2.3")
	require.NoError(t, err)
	shim.SessionID = "session-test"
	bridge := &stdioBridge{frames: make(chan []byte), writer: bufio.NewWriter(io.Discard)}
	var cancelled bool
	stream := strings.Join([]string{
		"event: message",
		`data: {"jsonrpc":"2.0","id":"elicit-1","method":"elicitation/create","params":{"mode":"form","message":"Approve?"}}`,
		"",
		"event: message",
		`data: {"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Approval pending: https://app.telara.dev/approvals?approval_id=approval-42"}]}}`,
		"",
	}, "\n")

	result, err := shim.readEventStream(context.Background(), strings.NewReader(stream), json.RawMessage("3"), bridge, func() { cancelled = true }, 2*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, cancelled, "missing UI response must not cancel the durable approval request")
	assert.Contains(t, string(result), "https://app.telara.dev/approvals?approval_id=approval-42")
}

func TestShim_ForwardsTheClientsBytesUnchanged(t *testing.T) {
	c := newCapture(t)

	// Key order and number formatting are deliberately awkward: re-marshalling
	// would normalise both, and anything hashing the raw frame would break.
	frame := `{"method":"tools/call","jsonrpc":"2.0","params":{"name":"x","arguments":{"b":1,"a":1.50,"z":"é"}},"id":7}`
	run(t, c, frame+"\n")

	bodies, _ := c.seen()
	require.Len(t, bodies, 1)
	assert.Equal(t, frame, bodies[0], "the frame must reach the gateway byte-for-byte")
}

func TestShim_DoesNotAnswerNotifications(t *testing.T) {
	c := newCapture(t)

	stdout, _ := run(t, c, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	bodies, _ := c.seen()
	assert.Len(t, bodies, 1, "a notification is still forwarded")
	assert.Empty(t, strings.TrimSpace(stdout), "answering a notification is a protocol violation")
}

func TestShim_MalformedInputIsReportedAndDoesNotStopTheSession(t *testing.T) {
	c := newCapture(t)

	stdout, stderr := run(t, c, strings.Join([]string{
		`{"jsonrpc":"2.0", NOT JSON`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	}, "\n")+"\n")

	assert.Contains(t, stderr, "malformed frame")
	bodies, _ := c.seen()
	assert.Len(t, bodies, 1, "the valid frame after it is still served")
	assert.Contains(t, stdout, `"result"`)
}

func TestShim_BlankLinesAreSkipped(t *testing.T) {
	c := newCapture(t)

	run(t, c, "\n\n"+`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n\n")

	bodies, _ := c.seen()
	assert.Len(t, bodies, 1)
}

func TestShim_TransportFailureBecomesAnErrorReplyNotASilentDrop(t *testing.T) {
	c := newCapture(t)
	shim, err := New("http://127.0.0.1:1/unreachable", "raw-key", "1.2.3")
	require.NoError(t, err)
	shim.HTTP = c.server.Client()

	var out, errOut strings.Builder
	require.NoError(t, shim.Run(context.Background(),
		strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`+"\n"), &out, &errOut))

	// A client waiting on an id it never gets back hangs; an error it can report.
	var reply struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply))
	assert.Equal(t, 9, reply.ID)
	assert.Contains(t, reply.Error.Message, "reach gateway")
}

func TestShim_GatewayJSONRPCErrorIsPassedThroughVerbatim(t *testing.T) {
	c := newCapture(t)
	c.status = http.StatusForbidden
	c.response = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"policy denied this action"}}`

	stdout, _ := run(t, c, `{"jsonrpc":"2.0","id":3,"method":"tools/call"}`+"\n")

	// The gateway's own wording is what the user needs to see, not a wrapper.
	assert.JSONEq(t, c.response, strings.TrimSpace(stdout))
}

func TestShim_NonJSONRPCErrorBodyIsWrappedWithItsStatus(t *testing.T) {
	c := newCapture(t)
	c.status = http.StatusBadGateway
	c.response = `<html>upstream unavailable</html>`

	stdout, _ := run(t, c, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`+"\n")

	var reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &reply))
	assert.Contains(t, reply.Error.Message, "502")
	assert.Contains(t, reply.Error.Message, "upstream unavailable")
}

func TestShim_EmptyGatewayResponseIsAnErrorNotAnEmptyResult(t *testing.T) {
	c := newCapture(t)
	c.response = ""

	stdout, _ := run(t, c, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`+"\n")

	var reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &reply))
	assert.Contains(t, reply.Error.Message, "empty response")
}

func TestShim_LargeFrameIsNotTruncated(t *testing.T) {
	c := newCapture(t)

	// Well past bufio.Scanner's 64KB default, which would otherwise cut a tool
	// call in half and surface as a parse error.
	big := strings.Repeat("x", 512<<10)
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{"body":"` + big + `"}}}`
	run(t, c, frame+"\n")

	bodies, _ := c.seen()
	require.Len(t, bodies, 1)
	assert.Equal(t, len(frame), len(bodies[0]))
}
