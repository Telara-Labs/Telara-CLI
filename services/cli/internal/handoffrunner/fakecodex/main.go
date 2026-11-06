// Command fakecodex is a test double for the external provider only: it speaks the
// part of the Codex app-server stdio protocol the handoff runner uses, so the
// runner can be proven end to end without a real Codex login. It is never a stand-in
// for any Telara service.
//
// Like Codex it refuses an account read when the home has no sign-in, and it
// initializes every required MCP server at thread start by calling it with the
// configured headers, failing the thread when one refuses. The brief selects what
// the turn does: "fake:complete" makes one tool call and completes, "fake:hang"
// keeps calling until interrupted, "fake:fail" fails. Like Codex, every turn
// first sends one model request to its openai_base_url (-c on its command line),
// offering the tools Codex would: the grant session's MCP helpers, plus the
// native shell tool unless it was started with --disable shell_tool, and a turn
// whose model request is refused fails. "debug models --bundled" prints a small
// catalog with a native apply_patch entry. Every call it makes and
// everything it was given is appended to fake-codex.jsonl in its home, with the
// Authorization header reduced to its scheme and a digest.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type server struct {
	url      string
	headers  map[string]string
	required bool
}

type fake struct {
	modelURL  string
	disabled  map[string]bool
	catalog   string
	mu        sync.Mutex
	out       *bufio.Writer
	home      string
	thread    string
	threads   int
	servers   map[string]server
	interrupt chan struct{}
	ended     bool
}

func (f *fake) send(m map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m["jsonrpc"] = "2.0"
	b, _ := json.Marshal(m)
	_, _ = f.out.Write(append(b, '\n'))
	_ = f.out.Flush()
}

func (f *fake) record(kind string, data map[string]any) {
	data["kind"], data["at"], data["pid"] = kind, time.Now().UTC().Format(time.RFC3339Nano), os.Getpid()
	b, _ := json.Marshal(data)
	file, err := os.OpenFile(filepath.Join(f.home, "fake-codex.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(b, '\n'))
}

// call is one MCP request to a configured server, as Codex would make it.
func (f *fake) call(name, method string) int {
	s := f.servers[name]
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{}})
	req, err := http.NewRequest(http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	code := 0
	if err == nil {
		code = resp.StatusCode
		resp.Body.Close()
	}
	f.record("mcp_call", map[string]any{"server": name, "method": method, "status": code})
	return code
}

func authDigest(headers map[string]string) (scheme string, grantSession bool, digest string) {
	scheme, token, _ := strings.Cut(headers["Authorization"], " ")
	sum := sha256.Sum256([]byte(token))
	return scheme, strings.HasPrefix(token, "telara_handoff_"), hex.EncodeToString(sum[:])
}

func (f *fake) threadStart(id any, params map[string]any) {
	cfg, _ := params["config"].(map[string]any)
	servers, _ := cfg["mcp_servers"].(map[string]any)
	f.servers = map[string]server{}
	names := []string{}
	for name, raw := range servers {
		m, _ := raw.(map[string]any)
		s := server{headers: map[string]string{}}
		s.url, _ = m["url"].(string)
		s.required, _ = m["required"].(bool)
		if h, ok := m["http_headers"].(map[string]any); ok {
			for k, v := range h {
				s.headers[k], _ = v.(string)
			}
		}
		f.servers[name] = s
		names = append(names, name)
	}
	rec := map[string]any{"servers": names, "sandbox": params["sandbox"], "approval_policy": params["approvalPolicy"], "ephemeral": params["ephemeral"], "cwd": params["cwd"], "config": map[string]any{"sandbox_workspace_write": cfg["sandbox_workspace_write"], "tools": cfg["tools"]}}
	for name, s := range f.servers {
		scheme, grant, digest := authDigest(s.headers)
		rec["auth_scheme_"+name], rec["auth_grant_session_"+name], rec["auth_digest_"+name], rec["required_"+name] = scheme, grant, digest, s.required
	}
	f.record("thread_start", rec)
	for name, s := range f.servers {
		if s.required {
			if code := f.call(name, "initialize"); code < 200 || code > 299 {
				f.send(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "required MCP server " + name + " failed to initialize"}})
				return
			}
		}
	}
	// Each thread/start is a new thread, as in Codex.
	f.threads++
	f.thread = fmt.Sprintf("fake-thread-%d", f.threads)
	f.send(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": f.thread}}})
}

func (f *fake) turn(mode string) {
	finish := func(status string) {
		f.mu.Lock()
		if f.ended {
			f.mu.Unlock()
			return
		}
		f.ended = true
		f.mu.Unlock()
		f.record("turn_end", map[string]any{"status": status})
		f.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": f.thread, "turn": map[string]any{"id": "turn-1", "status": status}}})
	}
	switch mode {
	case "complete":
		for name := range f.servers {
			f.call(name, "tools/call")
		}
		finish("completed")
	case "fail":
		finish("failed")
	default:
		for {
			select {
			case <-f.interrupt:
				finish("interrupted")
				return
			case <-time.After(200 * time.Millisecond):
				for name := range f.servers {
					f.call(name, "tools/call")
				}
			}
		}
	}
}

// parseArgs reads the app-server command line the way the runner writes it.
func (f *fake) parseArgs(args []string) {
	f.disabled = map[string]bool{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--disable":
			if i+1 < len(args) {
				f.disabled[args[i+1]] = true
				i++
			}
		case "-c":
			if i+1 < len(args) {
				k, v, _ := strings.Cut(args[i+1], "=")
				var s string
				if json.Unmarshal([]byte(v), &s) == nil {
					switch k {
					case "openai_base_url":
						f.modelURL = s
					case "model_catalog_json":
						f.catalog = s
					}
				}
				i++
			}
		}
	}
}

// modelRequest sends the turn's one model request and reports whether it was
// accepted.
func (f *fake) modelRequest(thread string) bool {
	if f.modelURL == "" {
		return true
	}
	tools := []any{
		map[string]any{"type": "function", "name": "list_mcp_resources"},
		map[string]any{"type": "tool_search"},
	}
	if !f.disabled["shell_tool"] {
		tools = append(tools, map[string]any{"type": "function", "name": "exec_command"})
	}
	body, _ := json.Marshal(map[string]any{"model": "fake", "tools": tools, "input": []any{}})
	req, err := http.NewRequest(http.MethodPost, f.modelURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("thread-id", thread)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	code := 0
	if err == nil {
		code = resp.StatusCode
		resp.Body.Close()
	}
	f.record("model_request", map[string]any{"status": code, "thread": thread, "catalog": f.catalog != ""})
	return code >= 200 && code <= 299
}

func main() {
	if len(os.Args) > 2 && os.Args[1] == "debug" && os.Args[2] == "models" {
		fmt.Println(`{"models":[{"slug":"fake","apply_patch_tool_type":"freeform","tool_mode":"code","multi_agent_version":"v2","experimental_supported_tools":["x"]}]}`)
		return
	}
	f := &fake{out: bufio.NewWriter(os.Stdout), home: os.Getenv("CODEX_HOME"), interrupt: make(chan struct{}, 1)}
	f.parseArgs(os.Args[1:])
	f.record("start", map[string]any{"args": os.Args[1:], "env": os.Environ()})
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		id, hasID := m["id"]
		method, _ := m["method"].(string)
		params, _ := m["params"].(map[string]any)
		if !hasID {
			continue
		}
		switch method {
		case "initialize":
			f.send(map[string]any{"id": id, "result": map[string]any{"userAgent": "fake_codex/0.0.0-fake (test double)"}})
		case "account/read":
			if _, err := os.Stat(filepath.Join(f.home, "auth.json")); err != nil {
				f.send(map[string]any{"id": id, "result": map[string]any{"account": nil, "requiresOpenaiAuth": true}})
			} else {
				f.send(map[string]any{"id": id, "result": map[string]any{"account": map[string]any{"type": "chatgpt", "email": "receiver@example.invalid"}}})
			}
		case "thread/start":
			f.threadStart(id, params)
		case "mcpServerStatus/list":
			data := []any{}
			for name := range f.servers {
				data = append(data, map[string]any{"name": name, "tools": map[string]any{}})
			}
			f.send(map[string]any{"id": id, "result": map[string]any{"data": data}})
		case "turn/start":
			text := ""
			if input, ok := params["input"].([]any); ok && len(input) > 0 {
				first, _ := input[0].(map[string]any)
				text, _ = first["text"].(string)
			}
			mode := "hang"
			for _, m := range []string{"complete", "fail", "hang"} {
				if strings.Contains(text, "fake:"+m) {
					mode = m
					break
				}
			}
			sum := sha256.Sum256([]byte(text))
			f.record("turn_start", map[string]any{"mode": mode, "text_digest": hex.EncodeToString(sum[:]), "names_task": strings.Contains(text, "Your task id: ")})
			f.send(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress"}}})
			thread := f.thread
			if th, _ := params["threadId"].(string); th != "" {
				thread = th
			}
			go func() {
				f.mu.Lock()
				f.thread = thread
				f.mu.Unlock()
				if !f.modelRequest(thread) {
					f.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn-1", "status": "failed"}}})
					f.record("turn_end", map[string]any{"status": "failed", "thread": thread})
					return
				}
				if text == "probe" {
					f.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn-p", "status": "completed"}}})
					return
				}
				f.turn(mode)
			}()
		case "turn/interrupt":
			f.record("interrupt", map[string]any{})
			select {
			case f.interrupt <- struct{}{}:
			default:
			}
			f.send(map[string]any{"id": id, "result": map[string]any{}})
		default:
			f.send(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unsupported in the test double: " + method}})
		}
	}
}
