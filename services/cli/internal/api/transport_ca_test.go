package api

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// serverCAFile writes a TLS test server's certificate as a CA file.
func serverCAFile(t *testing.T, srv *httptest.Server) (string, []byte) {
	t.Helper()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, caPEM
}

// TELARA_CA_CERT_PATH adds the CA to the system roots; it never replaces them,
// so a process that sets it still reaches public endpoints (TENG-3693 B12b).
func TestNewTransportAddsTheCAToTheSystemRoots(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	path, caPEM := serverCAFile(t, srv)
	t.Setenv("TELARA_INSECURE", "")
	t.Setenv("TELARA_CA_CERT_PATH", path)

	tr, ok := NewTransport().(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatalf("transport %#v", tr)
	}
	want, err := x509.SystemCertPool()
	if err != nil {
		t.Skipf("no system pool here: %v", err)
	}
	want.AppendCertsFromPEM(caPEM)
	if !tr.TLSClientConfig.RootCAs.Equal(want) {
		t.Fatal("the pool is not the system roots plus the configured CA")
	}
	onlyCA := x509.NewCertPool()
	onlyCA.AppendCertsFromPEM(caPEM)
	if tr.TLSClientConfig.RootCAs.Equal(onlyCA) {
		t.Fatal("the configured CA replaced the system roots")
	}
	// And the configured CA is trusted.
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatalf("the configured CA is not trusted: %v", err)
	}
	resp.Body.Close()
}

func TestWithAddedCARefusesABundleWithNoCertificate(t *testing.T) {
	if _, ok := withAddedCA([]byte("not a certificate")); ok {
		t.Fatal("accepted a bundle with no certificate")
	}
}
