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
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
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

// tapPublishCmd publishes a primitive that `tap discover` saved: the folder
// holds the package files, the SKILL.md pointer and the .tap-primitive.json
// marker that names it publisher/name. Saving already refused any draft that
// still held something credential-shaped.
var tapPublishCmd = &cobra.Command{
	Use:   "publish <saved-folder>",
	Short: "Publish a primitive saved by tap discover to your Telara tenant",
	Long: `Publish a primitive that tap discover saved, through the Telara MCP tool
telara_skill_publish. --audience user shares it with you only; tenant offers it
to everyone in the tenant once an admin approves it.`,
	Args: cobra.ExactArgs(1),
	RunE: runTapPublish,
}

func init() {
	tapPublishCmd.Flags().String("audience", "user", "Who gets it: user (only you) or tenant (everyone, after an admin approves it)")
	tapCmd.AddCommand(tapPublishCmd)
}

// savedMarker is the file `tap discover` writes into a saved primitive.
const savedMarker = ".tap-primitive.json"

// savedPrimitive is a saved folder read back for publishing.
type savedPrimitive struct {
	Publisher, Name, Summary string
	Files                    map[string][]byte
}

// readSavedPrimitive reads a folder saved by tap discover. The package is
// every file except the marker and the SKILL.md pointer the runner adds.
func readSavedPrimitive(dir string) (*savedPrimitive, error) {
	raw, err := os.ReadFile(filepath.Join(dir, savedMarker))
	if err != nil {
		return nil, fmt.Errorf("%s is not a primitive saved by tap discover (no %s)", dir, savedMarker)
	}
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: unreadable %s: %w", dir, savedMarker, err)
	}
	publisher, name, ok := strings.Cut(m.Name, "/")
	if !ok || publisher == "" || name == "" {
		return nil, fmt.Errorf("%s: %s names %q, not publisher/name", dir, savedMarker, m.Name)
	}
	sp := &savedPrimitive{Publisher: publisher, Name: name, Summary: name, Files: map[string][]byte{}}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || rel == savedMarker || rel == "SKILL.md" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sp.Files[rel] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := sp.Files["primitive.yaml"]; !ok {
		return nil, fmt.Errorf("%s has no primitive.yaml", dir)
	}
	if desc := strings.SplitN(string(sp.Files["README.md"]), "\n\n", 3); len(desc) > 1 {
		sp.Summary = strings.TrimSpace(desc[1])
	}
	return sp, nil
}

func runTapPublish(cmd *cobra.Command, args []string) error {
	audience, _ := cmd.Flags().GetString("audience")
	if audience != "user" && audience != "tenant" {
		return fmt.Errorf("--audience is user or tenant, not %q", audience)
	}
	sp, err := readSavedPrimitive(args[0])
	if err != nil {
		return err
	}
	key, err := auth.LoadMCPKey(prefs.APIURL)
	if err != nil {
		return fmt.Errorf("not signed in to Telara on this machine (run: telara login, then telara install)")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	audienceID := ""
	if audience == "user" {
		client, err := tapClient()
		if err != nil {
			return err
		}
		who, err := client.ValidateToken(ctx)
		if err != nil {
			return fmt.Errorf("find your user id: %w", err)
		}
		audienceID = who.UserID
	}
	m := &mcpCaller{endpoint: streamableDefaultMCPURL(), key: key}
	if err := m.initialize(ctx); err != nil {
		return err
	}
	text, isError, err := m.callTool(ctx, "telara_skill_publish", publishArgs(sp.Name, sp.Summary, sp.Publisher, audience, audienceID, sp.Files))
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), text)
	if isError {
		cmd.SilenceUsage = true
		return fmt.Errorf("telara_skill_publish refused %s/%s", sp.Publisher, sp.Name)
	}
	return nil
}
