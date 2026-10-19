package mcpstdio

import (
	"net/http"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/tlstest"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

// The stdio forwarder must trust the same private CA every other CLI request
// trusts (TENG-3499), and must keep verifying chain and hostname.
func TestDefaultHTTPClient_PrivateCATrust(t *testing.T) {
	ca := tlstest.NewCA(t, "telara-private-ca")
	good := ca.Serve(t, okHandler(), "127.0.0.1")

	t.Run("without the CA the private server is rejected", func(t *testing.T) {
		t.Setenv("TELARA_CA_CERT_PATH", "")
		_, err := DefaultHTTPClient().Get(good.URL)
		if !tlstest.IsVerifyFailure(err) {
			t.Fatalf("want certificate verification failure, got %v", err)
		}
	})
	t.Run("with the CA the private server is trusted", func(t *testing.T) {
		t.Setenv("TELARA_CA_CERT_PATH", ca.WriteBundle(t))
		resp, err := DefaultHTTPClient().Get(good.URL)
		if err != nil {
			t.Fatalf("trusted private CA must connect: %v", err)
		}
		resp.Body.Close()
	})
	t.Run("a CA that did not sign the server is not trusted", func(t *testing.T) {
		other := tlstest.NewCA(t, "someone-elses-ca")
		t.Setenv("TELARA_CA_CERT_PATH", other.WriteBundle(t))
		_, err := DefaultHTTPClient().Get(good.URL)
		if !tlstest.IsVerifyFailure(err) {
			t.Fatalf("want certificate verification failure, got %v", err)
		}
	})
	t.Run("a trusted CA does not excuse a hostname mismatch", func(t *testing.T) {
		wrong := ca.Serve(t, okHandler(), "other.example.test")
		t.Setenv("TELARA_CA_CERT_PATH", ca.WriteBundle(t))
		_, err := DefaultHTTPClient().Get(wrong.URL)
		if !tlstest.IsHostnameFailure(err) {
			t.Fatalf("want hostname verification failure, got %v", err)
		}
	})
}
