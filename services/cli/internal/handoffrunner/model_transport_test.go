package handoffrunner

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// The model endpoint is public. Telara's CA settings are for Telara and never
// change how the model proxy reaches it: a Telara CA once replaced the system
// roots for this connection and every run failed (TENG-3693 B12b). Telara's own
// connections, through the same settings, trust the CA.
func TestModelUpstreamUsesTheSystemRootsWhateverTelarasCASettings(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "telara-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELARA_CA_CERT_PATH", path)

	for _, insecure := range []string{"", "true"} {
		t.Setenv("TELARA_INSECURE", insecure)
		tr := modelUpstreamTransport()
		if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs != nil || tr.TLSClientConfig.InsecureSkipVerify {
			t.Fatalf("TELARA_INSECURE=%q: model transport TLS %#v, want the system roots", insecure, tr.TLSClientConfig)
		}
		// The Telara CA is not trusted for the model upstream.
		_, err := (&http.Client{Transport: tr}).Get(srv.URL)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("TELARA_INSECURE=%q: the model transport trusted Telara's CA: %v", insecure, err)
		}
	}

	// Telara's connections trust the CA (and keep the system roots; see api).
	t.Setenv("TELARA_INSECURE", "")
	resp, err := (&http.Client{Transport: api.NewTransport()}).Get(srv.URL)
	if err != nil {
		t.Fatalf("Telara's transport does not trust the configured CA: %v", err)
	}
	resp.Body.Close()
}

// The model proxy, given no transport, uses the system-roots transport.
func TestModelProxyDefaultsToTheSystemRootsTransport(t *testing.T) {
	p, err := startModelProxy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if p.forwardTransport == nil {
		t.Fatal("no transport")
	}
	tr, ok := p.forwardTransport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs != nil {
		t.Fatalf("model proxy transport %#v", p.forwardTransport)
	}
}
