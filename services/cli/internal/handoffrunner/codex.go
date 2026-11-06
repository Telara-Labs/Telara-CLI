package handoffrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// grantTools are the grant session's tools as the run's thread lists them.
	grantTools []string

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

// launch is how one Codex process starts.
type launch struct {
	Bin, Home, Workdir string
	// Catalog is the tool-free model catalog (writeToolFreeCatalog); "" omits it.
	Catalog string
	// ModelURL is the runner's model proxy, Codex's openai_base_url; "" omits it.
	ModelURL string
	// Confine, when set, starts the process under the platform's confinement.
	Confine *confinement
}

// args are app-server's command line: native tools off, the tool-free catalog,
// model traffic to the runner's model proxy, and Codex's own credentials kept in
// its home's files rather than the person's keychain.
func (l launch) args() []string {
	args := appServerArgs(l.Catalog)
	if l.ModelURL != "" {
		b, _ := json.Marshal(l.ModelURL)
		args = append(args, "-c", "openai_base_url="+string(b))
	}
	return append(args, "-c", `cli_auth_credentials_store="file"`, "-c", `mcp_oauth_credentials_store="file"`)
}

// startCodex starts app-server with only the dedicated home in its environment.
// Nothing of the person's own Codex configuration or environment is inherited, so
// no standing MCP server, profile or credential of theirs reaches the run.
func startCodex(l launch) (*codex, error) {
	cmd := exec.Command(l.Bin, l.args()...)
	cmd.Dir = l.Workdir
	tmp := ""
	if l.Confine != nil {
		tmp = l.Confine.Tmp
		if _, err := confineCommand(cmd, l.Confine); err != nil {
			return nil, err
		}
	}
	cmd.Env = childEnv(l.Home, tmp)
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
	kind, err := c.accountType()
	return kind != "", err
}

// accountType is how the runner's home is signed in ("apiKey", "chatgpt"), or ""
// when it is not.
func (c *codex) accountType() (string, error) {
	r, err := c.call("account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return "", err
	}
	account, _ := r["account"].(map[string]any)
	if len(account) == 0 {
		return "", nil
	}
	kind, _ := account["type"].(string)
	if kind == "" {
		kind = "unknown"
	}
	return kind, nil
}

// threadConfig is the whole configuration of a receiving thread. The grant session
// is its only MCP server, marked required so the thread fails to start rather than
// run without it. The server is the run's loopback proxy and authorization is the
// proxy's per-run token, so Codex never holds the session credential. Its tools are
// approved in Codex: the grant session decides each call, and Codex's own prompt
// could never be answered under approval never, which refused every call
// (TENG-3693 B12b). Every native
// tool is off (threadToolConfig) and the sandbox keeps writes to the run's own
// empty directory with no network. Neither is taken on trust: the tool surface is
// proven before any input (proveToolSurface) and checked on every model request,
// and the process itself is confined where the platform allows (confine.go).
func threadConfig(mcpURL, authorization string) map[string]any {
	cfg := threadToolConfig()
	cfg["mcp_servers"] = map[string]any{
		AttachmentName: map[string]any{
			"url":                         mcpURL,
			"http_headers":                map[string]string{"Authorization": authorization},
			"required":                    true,
			"default_tools_approval_mode": "approve",
		},
	}
	cfg["sandbox_workspace_write"] = map[string]any{"network_access": false}
	return cfg
}

// checkOnlyGrantServer fails closed unless the grant session is the thread's only
// MCP server, and names any other server it finds.
func checkOnlyGrantServer(status map[string]any) error {
	data, _ := status["data"].([]any)
	if len(data) != 1 {
		var others []string
		for _, d := range data {
			e, _ := d.(map[string]any)
			if n, _ := e["name"].(string); n != AttachmentName {
				others = append(others, fmt.Sprintf("%q", n))
			}
		}
		return fmt.Errorf("the thread has %d MCP servers, want only the grant session (unexpected: %s)", len(data), strings.Join(others, ", "))
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

// openThread opens one ephemeral thread with the run's configuration and returns
// its id and its MCP servers' status.
func (c *codex) openThread(workdir, mcpURL, authorization string) (string, map[string]any, error) {
	started, err := c.call("thread/start", map[string]any{
		"ephemeral": true, "cwd": workdir, "approvalPolicy": "never", "sandbox": "workspace-write",
		"config": threadConfig(mcpURL, authorization),
	})
	if err != nil {
		return "", nil, fmt.Errorf("the grant session could not be attached: %w", err)
	}
	var id string
	if th, ok := started["thread"].(map[string]any); ok {
		id, _ = th["id"].(string)
	}
	if id == "" {
		id, _ = started["threadId"].(string)
	}
	if id == "" {
		return "", nil, errors.New("codex did not name the thread")
	}
	status, err := c.call("mcpServerStatus/list", map[string]any{"threadId": id, "limit": 100})
	if err != nil {
		return "", nil, err
	}
	if err := checkOnlyGrantServer(status); err != nil {
		return "", nil, err
	}
	return id, status, nil
}

// startThread opens the run's thread bound to the grant session, with no input,
// and proves the session is its one and only attachment before anything is sent.
func (c *codex) startThread(workdir, mcpURL, authorization string) error {
	id, status, err := c.openThread(workdir, mcpURL, authorization)
	if err != nil {
		return err
	}
	c.thread, c.grantTools = id, grantToolNames(status)
	return nil
}

// probeTimeout bounds the tool-surface probe.
var probeTimeout = time.Minute

// proveToolSurface reads the tools this Codex process offers the model, before
// any input, and fails closed naming each one that is not the grant session's.
// It opens a second thread with the same configuration and sends it one fixed
// word; the model proxy answers that request itself, so nothing leaves the
// machine and the run's own thread carries no probe. It returns what was seen.
func (c *codex) proveToolSurface(workdir, mcpURL, authorization string, models *modelProxy) ([]toolRef, error) {
	id, _, err := c.openThread(workdir, mcpURL, authorization)
	if err != nil {
		return nil, fmt.Errorf("tool probe: %w", err)
	}
	seen := models.expectProbe(id)
	defer models.endProbe(id)
	if _, err := c.call("turn/start", map[string]any{"threadId": id, "input": []map[string]any{{"type": "text", "text": "probe"}}}); err != nil {
		return nil, fmt.Errorf("tool probe: %w", err)
	}
	var visible []toolRef
	select {
	case visible = <-seen:
	case <-c.exited:
		return nil, errors.New("tool probe: codex exited")
	case <-time.After(probeTimeout):
		return nil, errNoToolSurface
	}
	if err := checkToolSurface(visible, c.grantTools); err != nil {
		return visible, err
	}
	if err := checkGrantToolsOffered(visible, c.grantTools); err != nil {
		return visible, err
	}
	return visible, nil
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

// turnOutcome reads one notification as the end of a turn of the given thread,
// if it is one. A probe thread's turn is never read as the run's.
func turnOutcome(m map[string]any, thread string) (string, bool) {
	if m["method"] != "turn/completed" {
		return "", false
	}
	params, _ := m["params"].(map[string]any)
	if id, _ := params["threadId"].(string); id != "" && id != thread {
		return "", false
	}
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
			if status, ok := turnOutcome(m, c.thread); ok {
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
