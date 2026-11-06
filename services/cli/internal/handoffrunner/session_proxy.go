package handoffrunner

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/dpop"
)

// sessionProxy is the grant session as Codex sees it (TENG-3693 G05).
//
// The session is bound to the runner's key, so every MCP call must carry a DPoP
// proof by that key, and Codex cannot sign one. The runner therefore serves each
// run's attachment on a loopback port: Codex calls it with a random token that
// exists only for this run, and the proxy forwards the call to Telara with the
// session credential and a fresh proof. Codex never holds the credential or the
// key, so neither can leave through the agent, and a copy of the token is useless
// once the run ends and the port closes.
type sessionProxy struct {
	target     *url.URL
	credential string
	key        *dpop.Key
	local      string
	listener   net.Listener
	server     *http.Server
	logf       func(string, ...any)
}

// startSessionProxy serves one run's attachment on 127.0.0.1 until close.
func startSessionProxy(mcpURL, credential string, key *dpop.Key, transport http.RoundTripper, logf func(string, ...any)) (*sessionProxy, error) {
	if key == nil {
		return nil, errors.New("the runner holds no key (run: telara handoff runner setup)")
	}
	target, err := url.Parse(mcpURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, errors.New("the Telara MCP URL must be absolute")
	}
	target.RawQuery, target.Fragment = "", ""
	if target.Path == "" {
		target.Path = "/"
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if transport == nil {
		transport = api.NewTransport()
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := &sessionProxy{target: target, credential: credential, key: key, local: base64.RawURLEncoding.EncodeToString(token), listener: ln, logf: logf}
	forward := &httputil.ReverseProxy{
		Rewrite:       p.rewrite,
		Transport:     transport,
		FlushInterval: -1, // streamable HTTP answers with an event stream
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// The error names the upstream URL at most, never the credential or key.
			p.logf("grant session: Telara could not be reached: %v", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	p.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != target.Path {
			http.NotFound(w, r)
			return
		}
		if !p.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		forward.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

// URL is the attachment Codex is configured with.
func (p *sessionProxy) URL() string {
	return "http://" + p.listener.Addr().String() + p.target.EscapedPath()
}

// localAuthorization is the header Codex presents to the proxy.
func (p *sessionProxy) localAuthorization() string { return "Bearer " + p.local }

func (p *sessionProxy) authorized(r *http.Request) bool {
	got := r.Header.Get("Authorization")
	return subtle.ConstantTimeCompare([]byte(got), []byte(p.localAuthorization())) == 1
}

// rewrite sends the call to Telara with the credential and a proof made for this
// exact request. Nothing Codex sent that could authenticate is forwarded.
func (p *sessionProxy) rewrite(pr *httputil.ProxyRequest) {
	out := pr.Out
	out.URL.Scheme, out.URL.Host = p.target.Scheme, p.target.Host
	out.URL.Path, out.URL.RawPath = p.target.Path, p.target.RawPath
	out.Host = p.target.Host
	out.Header.Del("Cookie")
	out.Header.Del(dpop.Header)
	out.Header.Set("Authorization", "DPoP "+p.credential)
	proof, err := p.key.Proof(out.Method, p.target.String(), p.credential)
	if err != nil {
		// Without a proof the bound session refuses the call; nothing else is tried.
		p.logf("grant session: %v", err)
		return
	}
	out.Header.Set(dpop.Header, proof)
}

// close ends the attachment. Codex's token stops working with it.
func (p *sessionProxy) close() {
	if p == nil || p.server == nil {
		return
	}
	_ = p.server.Close()
}

// port is the loopback port, for the confinement's network rule.
func (p *sessionProxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }
