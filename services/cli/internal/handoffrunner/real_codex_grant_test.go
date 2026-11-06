package handoffrunner

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// A real model turn through the armed proxy with a grant session that has tools:
// the objective needs one, and the model must be offered it and call it through
// the grant session (TENG-3693 B12b). Native tools stay off and the tool check
// stays strict on every request.
func TestRealCodexTurnUsesTheGrantSessionsTools(t *testing.T) {
	r := realRunner(t)
	if err := r.checkHome(); err != nil {
		t.Fatal(err)
	}
	// A configured Telara CA must not stop the model upstream being reached: it
	// once replaced the system roots there and every run failed (B12b).
	ca := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ca.Close()
	caPath := filepath.Join(t.TempDir(), "telara-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELARA_CA_CERT_PATH", caPath)
	grant, err := startStandInGrant()
	if err != nil {
		t.Fatal(err)
	}
	defer grant.close()
	grant.tools = []any{map[string]any{
		"name":        "task_checkpoint",
		"description": "Record progress on your task. Call it whenever you make progress.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"task_id": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}},
			"required":   []any{"task_id", "summary"},
		},
	}}
	grant.calls = make(chan string, 8)
	models, err := startModelProxy(nil, r.logf)
	if err != nil {
		t.Fatal(err)
	}
	defer models.close()
	dir := t.TempDir()
	l, confined, err := r.launchFor(dir, models.URL(), models.port(), grant.port())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" && confined == "" {
		t.Fatal("not confined")
	}
	agent, err := startCodex(l)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.close()
	kind, err := agent.accountType()
	if err != nil || kind == "" {
		t.Fatalf("not signed in: %q %v", kind, err)
	}
	upstream, err := upstreamFor(kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.startThread(dir, grant.url(), grant.authorization()); err != nil {
		t.Fatal(err)
	}
	if len(agent.grantTools) != 1 || agent.grantTools[0] != "task_checkpoint" {
		t.Fatalf("grant tools %v", agent.grantTools)
	}
	visible, err := agent.proveToolSurface(dir, grant.url(), grant.authorization(), models)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probe saw %v", visible)
	if !offersGrantTool(visible, "task_checkpoint") {
		t.Fatalf("the probe's model request does not offer the grant session's tool: %v", visible)
	}
	if err := models.arm(agent.grantTools, upstream); err != nil {
		t.Fatal(err)
	}
	if err := agent.startTurn(firstTurn(standInInput(), "task-b12b")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var said strings.Builder
	used := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("the turn did not end")
		case <-agent.exited:
			t.Fatal("codex exited")
		case name := <-grant.calls:
			t.Logf("the grant session served tools/call %s", name)
			if name == "task_checkpoint" {
				used = true
			}
		case m := <-agent.events:
			method, _ := m["method"].(string)
			params, _ := m["params"].(map[string]any)
			item, _ := params["item"].(map[string]any)
			if method == "item/completed" {
				switch item["type"] {
				case "commandExecution", "fileChange", "webSearch", "imageView", "dynamicToolCall", "collabAgentToolCall":
					t.Fatalf("the model used a native tool: %v", item)
				case "mcpToolCall":
					t.Logf("mcp tool call: %v", item)
				case "agentMessage":
					text, _ := item["text"].(string)
					said.WriteString(text)
				}
			}
			if status, ok := turnOutcome(m, agent.thread); ok {
				t.Logf("turn %s; the model said %q", status, said.String())
				if status != "completed" {
					t.Fatalf("turn %s", status)
				}
				// A tools/call may land just before the turn's end is read.
				select {
				case name := <-grant.calls:
					used = used || name == "task_checkpoint"
				default:
				}
				if !used {
					t.Fatal("the model did not call the grant session's task_checkpoint")
				}
				return
			}
		}
	}
}

func standInInput() *api.HandoffRunInput {
	return &api.HandoffRunInput{BriefJSON: `{"objective":"Record one checkpoint on your task with the summary \"started\", then reply DONE."}`}
}

// offersGrantTool reports whether a model request offers the grant session's tool.
func offersGrantTool(visible []toolRef, name string) bool {
	for _, v := range visible {
		if grantTool(v, map[string]bool{name: true}) {
			return true
		}
	}
	return false
}
