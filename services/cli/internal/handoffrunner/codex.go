package handoffrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// AttachmentName is the one MCP server a receiving thread has: the grant session.
const AttachmentName = "telara_handoff"

// codex drives one Codex app-server process over its JSON-RPC stdio protocol, the
// interface Codex's own desktop app uses. One process serves one handoff run.
type codex struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	version string
	thread  string

	mu      sync.Mutex
	n       int
	pending map[int]chan map[string]any
	gone    bool
	events  chan map[string]any
	exited  chan struct{}
}

// callTimeout bounds one protocol call. A turn is not a call: it is watched
// through notifications for as long as the grant lasts.
var callTimeout = 2 * time.Minute

// startCodex starts app-server with only the dedicated home in its environment.
// Nothing of the person's own Codex configuration or environment is inherited, so
// no standing MCP server, profile or credential of theirs reaches the run.
func startCodex(bin, home, workdir string) (*codex, error) {
	var env []string
	for _, key := range []string{"PATH", "LANG", "TMPDIR", "SYSTEMROOT", "WINDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	// Codex reads its home from CODEX_HOME. This points it at the runner's own
	// isolated home; it is Codex's variable, set for the child process only.
	env = append(env, "CODEX_HOME="+home, "HOME="+home)
	cmd := exec.Command(bin, "app-server")
	cmd.Dir, cmd.Env = workdir, env
	isolateProcess(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	c := &codex{cmd: cmd, in: in, pending: map[int]chan map[string]any{}, events: make(chan map[string]any, 64), exited: make(chan struct{})}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	go c.read(sc)
	init, err := c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "telara-handoff-runner", "version": "1"}})
	if err != nil {
		c.close()
		return nil, err
	}
	if ua, _ := init["userAgent"].(string); ua != "" {
		if _, v, ok := strings.Cut(ua, "/"); ok {
			if f := strings.Fields(v); len(f) > 0 {
				c.version = f[0]
			}
		}
	}
	if c.version == "" {
		c.close()
		return nil, errors.New("codex app-server did not name its version")
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *codex) read(sc *bufio.Scanner) {
	defer close(c.exited)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if method, _ := m["method"].(string); method != "" {
			if _, isRequest := m["id"]; isRequest {
				// A request of the server's own, such as an approval. The thread runs
				// with approval never, so nothing here may approve anything: decline.
				c.reply(m["id"], map[string]any{"decision": "decline"})
				continue
			}
			select {
			case c.events <- m:
			default:
			}
			continue
		}
		idf, ok := m["id"].(float64)
		if !ok {
			continue
		}
		c.mu.Lock()
		ch := c.pending[int(idf)]
		delete(c.pending, int(idf))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.mu.Lock()
	c.gone = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *codex) write(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

func (c *codex) reply(id any, result map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *codex) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *codex) call(method string, params any) (map[string]any, error) {
	ch := make(chan map[string]any, 1)
	c.mu.Lock()
	if c.gone {
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: codex has exited", method)
	}
	c.n++
	id := c.n
	c.pending[id] = ch
	err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s: codex exited before it answered", method)
		}
		if e, ok := m["error"]; ok && e != nil {
			return nil, fmt.Errorf("%s: %v", method, e)
		}
		res, _ := m["result"].(map[string]any)
		return res, nil
	case <-time.After(callTimeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: codex did not answer", method)
	}
}

// signedIn reads the account with refresh off. Nil means not signed in: the person
// has not logged in to Codex inside the runner's own home.
func (c *codex) signedIn() (bool, error) {
	r, err := c.call("account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return false, err
	}
	account, _ := r["account"].(map[string]any)
	return len(account) > 0, nil
}

// threadConfig is the whole configuration of a receiving thread. The grant session
// is its only MCP server, marked required so the thread fails to start rather than
// run without it. The server is the run's loopback proxy and authorization is the
// proxy's per-run token, so Codex never holds the session credential. The sandbox keeps writes to the run's own empty directory with
// no network, and web search is off, so native tools cannot reach around the grant.
// Codex's sandbox is its own enforcement: whether it confines every native path for
// a given account and version is a qualification question, not settled here.
func threadConfig(mcpURL, authorization string) map[string]any {
	return map[string]any{
		"mcp_servers": map[string]any{
			AttachmentName: map[string]any{
				"url":          mcpURL,
				"http_headers": map[string]string{"Authorization": authorization},
				"required":     true,
			},
		},
		"sandbox_workspace_write": map[string]any{"network_access": false},
		"tools":                   map[string]any{"web_search": false},
		"web_search":              "disabled",
	}
}

// startThread opens one ephemeral thread bound to the grant session, with no input,
// and proves the session is its one and only attachment before anything is sent.
func (c *codex) startThread(workdir, mcpURL, authorization string) error {
	started, err := c.call("thread/start", map[string]any{
		"ephemeral": true, "cwd": workdir, "approvalPolicy": "never", "sandbox": "workspace-write",
		"config": threadConfig(mcpURL, authorization),
	})
	if err != nil {
		return fmt.Errorf("the grant session could not be attached: %w", err)
	}
	if th, ok := started["thread"].(map[string]any); ok {
		c.thread, _ = th["id"].(string)
	}
	if c.thread == "" {
		c.thread, _ = started["threadId"].(string)
	}
	if c.thread == "" {
		return errors.New("codex did not name the thread")
	}
	status, err := c.call("mcpServerStatus/list", map[string]any{"threadId": c.thread, "limit": 100})
	if err != nil {
		return err
	}
	data, _ := status["data"].([]any)
	if len(data) != 1 {
		return fmt.Errorf("the thread has %d MCP servers, want only the grant session", len(data))
	}
	if next, _ := status["nextCursor"].(string); next != "" {
		return errors.New("the thread has more MCP servers than the grant session")
	}
	entry, _ := data[0].(map[string]any)
	if name, _ := entry["name"].(string); name != AttachmentName {
		return fmt.Errorf("the thread's MCP server is %q, not the grant session", name)
	}
	return nil
}

// startTurn sends the first and only input.
func (c *codex) startTurn(text string) error {
	_, err := c.call("turn/start", map[string]any{"threadId": c.thread, "input": []map[string]any{{"type": "text", "text": text}}})
	return err
}

// interrupt asks the running turn to stop. The process is killed after it either way.
func (c *codex) interrupt() {
	if c.thread == "" {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = c.call("turn/interrupt", map[string]any{"threadId": c.thread})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// turnOutcome reads one notification as the end of the turn, if it is one.
func turnOutcome(m map[string]any) (string, bool) {
	if m["method"] != "turn/completed" {
		return "", false
	}
	params, _ := m["params"].(map[string]any)
	turn, _ := params["turn"].(map[string]any)
	status, _ := turn["status"].(string)
	if status == "" {
		status, _ = params["status"].(string)
	}
	return status, true
}

// waitTurn waits for the turn to end, the process to exit or ctx to end.
func (c *codex) waitTurn(ctx context.Context) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-c.exited:
			return "", errors.New("codex exited during the turn")
		case m := <-c.events:
			if status, ok := turnOutcome(m); ok {
				return status, nil
			}
		}
	}
}

func (c *codex) pid() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// close ends the process and everything it started.
func (c *codex) close() {
	if c.in != nil {
		_ = c.in.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		killTree(c.cmd.Process.Pid)
		_ = c.cmd.Wait()
	}
}
