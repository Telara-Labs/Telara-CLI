package handoffrunner

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Check starts Codex exactly as a run would, with a local stand-in for the grant
// session in place of a real one, and reports what the agent could do (TENG-3693
// B11): the confinement in force, the tools Codex offers the model, and the
// outcome of native reads and writes the app-server is asked to make outside the
// run's directory. The stand-in has one tool, which must be offered to the model
// directly. It needs no Telara login and sends nothing to a model: the
// probe turn is answered by the runner's model proxy. It returns an error unless
// the tool surface is the grant session's alone and every native attempt failed.
func (r *Runner) Check(w io.Writer) error {
	if err := r.checkHome(); err != nil {
		return err
	}
	grant, err := startStandInGrant()
	if err != nil {
		return err
	}
	defer grant.close()
	// One stand-in tool, so the probe also proves the grant session's tools reach
	// the model directly rather than deferred behind a search tool (B12b).
	grant.tools = []any{map[string]any{
		"name": checkGrantTool, "description": "The runner check's stand-in grant tool.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}}
	models, err := startModelProxy(nil, r.logf)
	if err != nil {
		return err
	}
	defer models.close()
	dir := filepath.Join(r.Dir, "runs", "runner-check")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	l, confined, err := r.launchFor(dir, models.URL(), models.port(), grant.port())
	if err != nil {
		return err
	}
	agent, err := startCodex(l)
	if err != nil {
		return err
	}
	defer agent.close()
	fmt.Fprintf(w, "Codex: %s (%s)\n", agent.version, r.CodexBin)
	if confined == "" {
		name, _ := confinementAvailable()
		fmt.Fprintf(w, "Process confinement: none (%s)\n", name)
	} else {
		fmt.Fprintf(w, "Process confinement: %s\n", confined)
		if confined == "bubblewrap" {
			fmt.Fprintln(w, "  network: shared with the machine; constrained by the tool surface below, not by the confinement")
		}
	}
	kind, err := agent.accountType()
	switch {
	case err != nil:
		fmt.Fprintf(w, "Codex sign-in: unreadable (%v)\n", err)
	case kind == "":
		fmt.Fprintln(w, "Codex sign-in: not signed in (run: telara handoff runner login)")
	default:
		fmt.Fprintf(w, "Codex sign-in: %s\n", kind)
	}

	var failures []string
	if err := agent.startThread(dir, grant.url(), grant.authorization()); err != nil {
		return fmt.Errorf("the stand-in grant session could not be attached: %w", err)
	}
	fmt.Fprintf(w, "MCP servers on the thread: %s only\n", AttachmentName)
	visible, err := agent.proveToolSurface(dir, grant.url(), grant.authorization(), models)
	fmt.Fprintf(w, "Tools offered to the model (%d):\n", len(visible))
	for _, t := range visible {
		fmt.Fprintf(w, "  %s\n", t)
	}
	if err != nil {
		fmt.Fprintf(w, "Tool surface: FAILED: %v\n", err)
		failures = append(failures, "tool surface")
	} else {
		fmt.Fprintln(w, "Tool surface: only the grant session's tools and Codex's MCP-only helpers")
	}

	// The control: the same requests inside the run's own directory must work, or
	// a refusal below would say nothing about the confinement.
	control := filepath.Join(dir, "control")
	_, werr := agent.call("fs/writeFile", map[string]any{"path": control, "dataBase64": base64.StdEncoding.EncodeToString([]byte("ok"))})
	got, rerr := agent.call("fs/readFile", map[string]any{"path": control})
	if data, _ := got["dataBase64"].(string); werr != nil || rerr != nil || data != base64.StdEncoding.EncodeToString([]byte("ok")) {
		return fmt.Errorf("inconclusive: Codex could not write and read its own run directory (write: %v, read: %v)", werr, rerr)
	}
	fmt.Fprintln(w, "Control, write and read inside the run's directory: allowed")
	fmt.Fprintln(w, "Native attempts outside the run's directory:")
	for _, a := range nativeAttempts() {
		res, err := agent.call(a.method, a.params)
		refused := err != nil || a.refused(res)
		if a.cleanup != nil {
			a.cleanup()
		}
		if refused {
			why := "refused"
			if err != nil {
				why = "refused: " + firstLine(err.Error())
			}
			fmt.Fprintf(w, "  %-40s %s\n", a.name, why)
			continue
		}
		fmt.Fprintf(w, "  %-40s ALLOWED\n", a.name)
		failures = append(failures, a.name)
	}
	if len(failures) > 0 {
		return fmt.Errorf("the runner's Codex is not contained: %s", strings.Join(failures, ", "))
	}
	fmt.Fprintln(w, "Result: contained")
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return s
}

// nativeAttempt is one machine-touching request the check makes of app-server.
type nativeAttempt struct {
	name    string
	method  string
	params  map[string]any
	refused func(map[string]any) bool
	cleanup func()
}

// nativeAttempts are the reads, the listing, the write and the command the live
// finding made through app-server, plus the person's own home directory.
func nativeAttempts() []nativeAttempt {
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	probeFile := filepath.Join(os.TempDir(), "telara-runner-check-"+hex.EncodeToString(suffix))
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		probeFile = filepath.Join(real, filepath.Base(probeFile))
	}
	attempts := []nativeAttempt{
		{name: "read /etc/passwd", method: "fs/readFile", params: map[string]any{"path": "/etc/passwd"}},
		{name: "list /Users", method: "fs/readDirectory", params: map[string]any{"path": "/Users"}},
		{name: "write " + probeFile, method: "fs/writeFile", params: map[string]any{"path": probeFile, "dataBase64": base64.StdEncoding.EncodeToString([]byte("x"))},
			// A write counts as allowed only if the file is there afterwards.
			refused: func(map[string]any) bool { _, err := os.Lstat(probeFile); return err != nil },
			cleanup: func() { _ = os.Remove(probeFile) }},
		{name: "run /bin/cat /etc/passwd", method: "command/exec", params: map[string]any{"command": []string{"/bin/cat", "/etc/passwd"}, "timeoutMs": 10000},
			refused: commandFailed},
	}
	if home, err := os.UserHomeDir(); err == nil {
		attempts = append(attempts, nativeAttempt{name: "list " + home, method: "fs/readDirectory", params: map[string]any{"path": home}})
	}
	for i := range attempts {
		if attempts[i].refused == nil {
			attempts[i].refused = func(map[string]any) bool { return false }
		}
	}
	return attempts
}

// commandFailed reads a command/exec answer: a command counts as run only when it
// exited 0 and printed something.
func commandFailed(res map[string]any) bool {
	code, _ := res["exitCode"].(float64)
	out, _ := res["stdout"].(string)
	return code != 0 || strings.TrimSpace(out) == ""
}

// checkGrantTool is the stand-in grant session's one tool in the runner check.
const checkGrantTool = "runner_check_noop"

// standInGrant is a local MCP server named like the grant session. It lists the
// tools it is given (none by default), so every other tool the model is offered
// is Codex's own and the surface check decides on Codex alone. It records the
// name of each tools/call when calls is set.
type standInGrant struct {
	listener net.Listener
	server   *http.Server
	token    string
	tools    []any       // tools/list; nil lists none
	calls    chan string // the name of each tools/call, when set
}

func startStandInGrant() (*standInGrant, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &standInGrant{listener: ln, token: base64.RawURLEncoding.EncodeToString(b)}
	g.server = &http.Server{Handler: http.HandlerFunc(g.serve), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = g.server.Serve(ln) }()
	return g, nil
}

func (g *standInGrant) url() string           { return "http://" + g.listener.Addr().String() + "/mcp" }
func (g *standInGrant) port() int             { return g.listener.Addr().(*net.TCPAddr).Port }
func (g *standInGrant) authorization() string { return "Bearer " + g.token }
func (g *standInGrant) close()                { _ = g.server.Close() }

func (g *standInGrant) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp" || r.Header.Get("Authorization") != g.authorization() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id, ok := req["id"]
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	method, _ := req["method"].(string)
	var result map[string]any
	switch method {
	case "initialize":
		params, _ := req["params"].(map[string]any)
		version, _ := params["protocolVersion"].(string)
		if version == "" {
			version = "2025-06-18"
		}
		result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "telara-runner-check", "version": "1"}}
	case "tools/list":
		tools := g.tools
		if tools == nil {
			tools = []any{}
		}
		result = map[string]any{"tools": tools}
	case "tools/call":
		if g.tools == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "not served by the check"}})
			return
		}
		params, _ := req["params"].(map[string]any)
		name, _ := params["name"].(string)
		if g.calls != nil {
			select {
			case g.calls <- name:
			default:
			}
		}
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "recorded"}}}
	case "resources/list":
		result = map[string]any{"resources": []any{}}
	case "resources/templates/list":
		result = map[string]any{"resourceTemplates": []any{}}
	case "ping":
		result = map[string]any{}
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "not served by the check"}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
