package cmd

import (
	"net/http"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/tlstest"
)

// doctor's connectivity probe must use the shared verified transport
// (TENG-3499): pass for a server signed by the configured private CA, fail for
// an untrusted CA and for a hostname mismatch.
func TestCheckConnectivity_PrivateCATrust(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	ca := tlstest.NewCA(t, "telara-private-ca")
	srv := ca.Serve(t, ok, "127.0.0.1")

	t.Setenv("TELARA_CA_CERT_PATH", "")
	if got := checkConnectivity(srv.URL); got.status != "fail" || !strings.Contains(got.message, "failed to verify certificate") {
		t.Fatalf("without the CA want fail/verification failure, got %+v", got)
	}

	t.Setenv("TELARA_CA_CERT_PATH", ca.WriteBundle(t))
	if got := checkConnectivity(srv.URL); got.status != "pass" {
		t.Fatalf("private CA must pass, got %+v", got)
	}

	wrong := ca.Serve(t, ok, "other.example.test")
	if got := checkConnectivity(wrong.URL); got.status != "fail" {
		t.Fatalf("hostname mismatch must fail, got %+v", got)
	}

	other := tlstest.NewCA(t, "someone-elses-ca")
	t.Setenv("TELARA_CA_CERT_PATH", other.WriteBundle(t))
	if got := checkConnectivity(srv.URL); got.status != "fail" {
		t.Fatalf("untrusted CA must fail, got %+v", got)
	}
}
