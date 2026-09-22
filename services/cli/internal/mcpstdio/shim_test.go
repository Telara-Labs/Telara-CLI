package mcpstdio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

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
		c.mu.Lock()
		c.bodies = append(c.bodies, string(body))
		c.headers = append(c.headers, r.Header.Clone())
		status, response := c.status, c.response
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
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

func TestShim_AnswersInitializeLocallyAndNeverForwardsIt(t *testing.T) {
	c := newCapture(t)

	stdout, _ := run(t, c, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"claude-code"}}}`+"\n")

	bodies, _ := c.seen()
	assert.Empty(t, bodies, "forwarding initialize would select the gateway's legacy session path")

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
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &reply))
	assert.Equal(t, 1, reply.ID)
	assert.Equal(t, "2025-06-18", reply.Result.ProtocolVersion, "the client's version is echoed, not overridden")
	assert.Equal(t, "telara", reply.Result.ServerInfo.Name)
	assert.Equal(t, "1.2.3", reply.Result.ServerInfo.Version)
	assert.Contains(t, reply.Result.Capabilities, "tools")
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
