package dpop

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// verify checks a proof the way RFC 9449 section 4.3 does, independently of the
// signer: the signature by the embedded key, typ, alg, htm, htu, iat and ath. The
// same proofs are verified by telara_security.VerifyDPoPProof, the function the
// Gateway calls, in agent-service's TestPGLocalRunner* against the real CLI binary.
func verify(t *testing.T, proof, method, htu, token string) (jkt string, claims map[string]any) {
	t.Helper()
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		t.Fatalf("proof has %d parts", len(parts))
	}
	decode := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("not base64url: %v", err)
		}
		return b
	}
	var header struct {
		Typ string            `json:"typ"`
		Alg string            `json:"alg"`
		JWK map[string]string `json:"jwk"`
	}
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Typ != "dpop+jwt" || header.Alg != "ES256" || header.JWK["kty"] != "EC" || header.JWK["crv"] != "P-256" {
		t.Fatalf("header %+v", header)
	}
	if _, ok := header.JWK["d"]; ok || len(header.JWK) != 4 {
		t.Fatalf("the proof's key carries more than the public members: %v", header.JWK)
	}
	raw := append([]byte{4}, append(decode(header.JWK["x"]), decode(header.JWK["y"])...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatal(err)
	}
	sig := decode(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the signature does not verify with the embedded key")
	}
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	ath := sha256.Sum256([]byte(token))
	if claims["htm"] != method || claims["htu"] != htu || claims["ath"] != base64.RawURLEncoding.EncodeToString(ath[:]) || claims["jti"] == "" {
		t.Fatalf("claims %v", claims)
	}
	if iat, _ := claims["iat"].(float64); time.Since(time.Unix(int64(iat), 0)) > 5*time.Second {
		t.Fatalf("iat %v", claims["iat"])
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(`{"crv":"P-256","kty":"EC","x":%q,"y":%q}`, header.JWK["x"], header.JWK["y"])))
	return base64.RawURLEncoding.EncodeToString(sum[:]), claims
}

func TestProofVerifiesAndNamesTheKeysThumbprint(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := k.Proof("POST", "https://api.example.test/v1/mcp?x=1#f", "telara_handoff_t.i.s")
	if err != nil {
		t.Fatal(err)
	}
	jkt, first := verify(t, proof, "POST", "https://api.example.test/v1/mcp", "telara_handoff_t.i.s")
	if jkt != k.Thumbprint() || len(jkt) != 43 {
		t.Fatalf("thumbprint %q, key says %q", jkt, k.Thumbprint())
	}
	again, _ := k.Proof("POST", "https://api.example.test/v1/mcp", "telara_handoff_t.i.s")
	_, second := verify(t, again, "POST", "https://api.example.test/v1/mcp", "telara_handoff_t.i.s")
	if first["jti"] == second["jti"] {
		t.Fatal("two proofs share a jti; the Gateway's replay ledger would refuse the second")
	}
}

func TestKeyFileIsOwnerOnlyAndSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "runner-dpop-key.pem")
	created, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v %v", info.Mode(), err)
	}
	loaded, err := LoadOrCreate(path)
	if err != nil || loaded.Thumbprint() != created.Thumbprint() {
		t.Fatalf("reload gave a different key: %v", err)
	}
	if _, err := Create(path); err == nil {
		t.Fatal("Create replaced an existing key")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a key others can read was accepted")
	}
}

func TestKeyNeverPrintsOrSerialises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.pem")
	k, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, _ := os.ReadFile(path)
	body := strings.Join(strings.Split(strings.TrimSpace(string(pemBytes)), "\n")[1:], "")
	body = strings.TrimSuffix(body, "-----END PRIVATE KEY-----")
	for _, out := range []string{fmt.Sprint(k), fmt.Sprintf("%v %+v %#v %s", k, k, k, k), fmt.Sprintf("%v", struct{ K *Key }{k})} {
		if strings.Contains(out, body[:24]) || strings.Contains(out, "PRIVATE") || !strings.Contains(out, k.Thumbprint()) {
			t.Fatalf("printed key: %s", out)
		}
	}
	if b, err := json.Marshal(struct{ K *Key }{k}); err == nil {
		t.Fatalf("a key was serialised: %s", b)
	}
}
