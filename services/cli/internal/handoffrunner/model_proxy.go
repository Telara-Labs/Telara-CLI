package handoffrunner

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// Codex's model endpoints, by how the runner's home is signed in.
const (
	chatGPTModelUpstream = "https://chatgpt.com/backend-api/codex"
	apiKeyModelUpstream  = "https://api.openai.com/v1"
)

// maxModelRequest bounds one model request body the guard reads.
const maxModelRequest = 64 << 20

// modelProxy carries Codex's model traffic (TENG-3693 B11). Codex is pointed at
// it with openai_base_url, so every model request passes through the runner,
// which reads the tools offered in it (checkToolSurface) and forwards only
// requests whose tools are the grant session's, to one fixed upstream. Under the
// macOS confinement this proxy and the session proxy are the only addresses
// Codex can reach, so the model traffic cannot go around it.
//
// A probe thread's requests are answered here and never forwarded: they exist
// only to read what Codex would offer the model before any input is fetched.
type modelProxy struct {
	upstream *url.URL // nil: nothing is forwarded (the runner's self-check)
	secret   string   // path segment only Codex is told
	listener net.Listener
	server   *http.Server
	logf     func(string, ...any)

	mu     sync.Mutex
	armed  bool
	grant  []string
	probes map[string]chan []toolRef
}

// startModelProxy serves Codex's model endpoint on 127.0.0.1 until close. It
// forwards nothing until arm names the grant session's tools and the upstream.
func startModelProxy(transport http.RoundTripper, logf func(string, ...any)) (*modelProxy, error) {
	p := &modelProxy{probes: map[string]chan []toolRef{}, logf: logf}
	if p.logf == nil {
		p.logf = func(string, ...any) {}
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	p.secret = base64.RawURLEncoding.EncodeToString(secret)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.listener = ln
	if transport == nil {
		transport = api.NewTransport()
	}
	forward := &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		Transport:     transport,
		FlushInterval: -1, // model answers stream as server-sent events
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			p.logf("model proxy: the model endpoint could not be reached: %v", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	p.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.serve(w, r, forward)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

// URL is the openai_base_url Codex is configured with.
func (p *modelProxy) URL() string {
	return "http://" + p.listener.Addr().String() + "/m/" + p.secret
}

// port is the loopback port, for the confinement's network rule.
func (p *modelProxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

// upstreamFor is the model endpoint for how the runner's home is signed in.
// Any other sign-in is refused: the runner knows no other endpoint to allow.
func upstreamFor(accountType string) (string, error) {
	switch accountType {
	case "apiKey":
		return apiKeyModelUpstream, nil
	case "chatgpt":
		return chatGPTModelUpstream, nil
	}
	return "", fmt.Errorf("the runner's Codex home is signed in as %q; the runner supports a ChatGPT or API key sign-in", accountType)
}

// arm sets the grant session's tools and the one upstream checked requests go
// to. Until then no request is forwarded. An empty upstream forwards nothing.
func (p *modelProxy) arm(grantTools []string, upstream string) error {
	var u *url.URL
	if upstream != "" {
		var err error
		u, err = url.Parse(upstream)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("the model upstream must be an absolute https URL")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed, p.grant, p.upstream = true, append([]string(nil), grantTools...), u
	return nil
}

// expectProbe marks a thread as a probe and returns where its tools arrive.
func (p *modelProxy) expectProbe(threadID string) <-chan []toolRef {
	ch := make(chan []toolRef, 1)
	p.mu.Lock()
	p.probes[threadID] = ch
	p.mu.Unlock()
	return ch
}

func (p *modelProxy) endProbe(threadID string) {
	p.mu.Lock()
	delete(p.probes, threadID)
	p.mu.Unlock()
}

// requestThread is the thread a model request belongs to, from Codex's headers.
func requestThread(r *http.Request, body map[string]any) string {
	for _, h := range []string{"thread-id", "session-id", "session_id"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	md, _ := body["client_metadata"].(map[string]any)
	v, _ := md["thread_id"].(string)
	return v
}

func (p *modelProxy) serve(w http.ResponseWriter, r *http.Request, forward http.Handler) {
	prefix := "/m/" + p.secret
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Upgrade") != "" || strings.EqualFold(r.Header.Get("Connection"), "upgrade") {
		// A WebSocket carries the model request in frames the guard does not read,
		// so it is refused; Codex then sends each request over plain HTTP.
		p.logf("model proxy: refused a %s upgrade the guard cannot read", r.Header.Get("Upgrade"))
		http.Error(w, "upgrades are refused", http.StatusUpgradeRequired)
		return
	}
	if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		// Codex compresses request bodies unless told not to (it is). A body the
		// guard cannot read is not forwarded.
		p.logf("model proxy: refused a %s-encoded model request the guard cannot read", enc)
		http.Error(w, "encoded request bodies are refused", http.StatusUnsupportedMediaType)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxModelRequest+1))
	if err != nil || len(raw) > maxModelRequest {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	var body map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "the guard reads JSON model requests only", http.StatusUnsupportedMediaType)
			return
		}
	}
	visible := modelVisibleTools(body)
	thread := requestThread(r, body)

	p.mu.Lock()
	probe, isProbe := p.probes[thread]
	armed, grant, upstream := p.armed, p.grant, p.upstream
	p.mu.Unlock()
	if isProbe && thread != "" && r.Method == http.MethodPost && body != nil {
		select {
		case probe <- visible:
		default:
		}
		writeProbeAnswer(w)
		return
	}
	if !armed {
		p.logf("model proxy: refused a model request before the thread's tools were checked")
		http.Error(w, "the runner has not checked this thread's tools", http.StatusForbidden)
		return
	}
	if err := checkToolSurface(visible, grant); err != nil {
		p.logf("model proxy: refused a model request: %v", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if upstream == nil {
		http.Error(w, "this runner forwards no model traffic", http.StatusServiceUnavailable)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	forward.ServeHTTP(w, r)
}

// rewrite sends a checked request to the one upstream, keeping Codex's own model
// authorization (its sign-in) and nothing that names this machine's proxy.
func (p *modelProxy) rewrite(pr *httputil.ProxyRequest) {
	out := pr.Out
	p.mu.Lock()
	up := p.upstream
	p.mu.Unlock()
	rest := strings.TrimPrefix(pr.In.URL.Path, "/m/"+p.secret)
	out.URL.Scheme, out.URL.Host = up.Scheme, up.Host
	out.URL.Path = strings.TrimSuffix(up.Path, "/") + rest
	out.URL.RawPath = ""
	out.Host = up.Host
	out.Header.Del("Cookie")
}

// writeProbeAnswer ends a probe turn at once with an empty assistant message.
func writeProbeAnswer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": "telara-probe"}},
		{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": "telara-probe-message", "content": []any{map[string]any{"type": "output_text", "text": ""}}}},
		{"type": "response.completed", "response": map[string]any{"id": "telara-probe", "usage": map[string]any{
			"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
			"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *modelProxy) close() {
	if p == nil || p.server == nil {
		return
	}
	_ = p.server.Close()
}
