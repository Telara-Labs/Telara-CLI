package cmd

// tap_publish.go publishes a drafted primitive (TENG-3059) through the Telara
// MCP tool telara_skill_publish: the governed client path, recorded in the
// gateway's session audit like any other MCP action. The REST publish route
// (POST /v1/tap/primitives) accepts only dashboard JWTs, and a second route
// would be a second surface to keep in step with this one.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/version"
)

// mcpCaller is a minimal streamable-HTTP MCP client: initialize, then calls.
type mcpCaller struct {
	endpoint, key string
	http          *http.Client
	session       string
	protocol      string
	nextID        int
}

func (m *mcpCaller) post(ctx context.Context, body map[string]any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+m.key)
	if m.session != "" {
		req.Header.Set("Mcp-Session-Id", m.session)
	}
	if m.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", m.protocol)
	}
	client := m.http
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// rpc sends one request and returns its result, reading either a JSON body
// or an event stream that carries the response.
func (m *mcpCaller) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	m.nextID++
	id := m.nextID
	resp, err := m.post(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, fmt.Errorf("reach the Telara MCP endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if method == "initialize" {
		m.session = strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	}
	var frames [][]byte
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data:") {
				frames = append(frames, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
			}
		}
	} else {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		frames = [][]byte{b}
	}
	for _, f := range frames {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(f, &msg) != nil || string(msg.ID) != fmt.Sprint(id) {
			if msg.Method == "elicitation/create" {
				return nil, errors.New("the gateway asked for an approval this command cannot show; publish from a client that supports approvals, or approve it in the dashboard")
			}
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
	return nil, fmt.Errorf("%s: no response in the reply", method)
}

func (m *mcpCaller) initialize(ctx context.Context) error {
	res, err := m.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "telara-cli", "version": version.Version},
	})
	if err != nil {
		return err
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(res, &r)
	m.protocol = r.ProtocolVersion
	resp, err := m.post(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if err == nil {
		resp.Body.Close()
	}
	return nil
}

// callTool runs a tool and returns its text. isError is the tool's own
// verdict (a refused publish), not a transport failure.
func (m *mcpCaller) callTool(ctx context.Context, name string, args map[string]any) (text string, isError bool, err error) {
	res, err := m.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", false, err
	}
	var r struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", false, fmt.Errorf("%s: unreadable result: %w", name, err)
	}
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n"), r.IsError, nil
}

// publishArgs is what telara_skill_publish is sent for a draft.
func publishArgs(name, description, publisher, audience, audienceID string, files map[string][]byte) map[string]any {
	fs := map[string]any{}
	for k, v := range files {
		fs[k] = string(v)
	}
	args := map[string]any{
		"name":              name,
		"description":       description,
		"publisher":         publisher,
		"files":             fs,
		"scope":             "team",
		"target_scope_type": audience,
	}
	if audienceID != "" {
		args["target_scope_id"] = audienceID
	}
	return args
}
