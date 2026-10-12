package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

func keyOf(version string) skillshare.SigningKey {
	return skillshare.SigningKey{KeyID: "tap-attestation:" + version, Version: version, Alg: "ed25519", PublicKeyB64: "AAAA"}
}

func TestSigningKeysForUsesAFreshCacheWithoutTheNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	now := time.Now()
	calls := 0
	fetch := func(context.Context) ([]skillshare.SigningKey, error) {
		calls++
		return []skillshare.SigningKey{keyOf("v1")}, nil
	}
	if _, err := signingKeysFor(context.Background(), path, "https://x", "v1", now, fetch); err != nil || calls != 1 {
		t.Fatalf("first: %v calls=%d", err, calls)
	}
	if _, err := signingKeysFor(context.Background(), path, "https://x", "v1", now.Add(time.Hour), fetch); err != nil || calls != 1 {
		t.Fatalf("a fresh cache holding the version must not refetch: %v calls=%d", err, calls)
	}
}

func TestSigningKeysForRefetchesOnRotationAndWhenStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	now := time.Now()
	calls := 0
	fetch := func(context.Context) ([]skillshare.SigningKey, error) {
		calls++
		return []skillshare.SigningKey{keyOf("v1"), keyOf("v2")}, nil
	}
	_ = writeKeyCache(path, signingKeyCache{Endpoint: "https://x", FetchedAt: now, Keys: []skillshare.SigningKey{keyOf("v1")}})
	keys, err := signingKeysFor(context.Background(), path, "https://x", "v2", now, fetch)
	if err != nil || calls != 1 || !hasKeyVersion(keys, "v2") {
		t.Fatalf("an unknown version must refetch: %v calls=%d", err, calls)
	}
	if _, err := signingKeysFor(context.Background(), path, "https://x", "v1", now.Add(keyCacheTTL+time.Minute), fetch); err != nil || calls != 2 {
		t.Fatalf("a stale cache must refetch: %v calls=%d", err, calls)
	}
	c, ok := readKeyCache(path, "https://x")
	if !ok || c.FetchedAt.IsZero() {
		t.Fatal("the cache must record when it was fetched")
	}
	if _, ok := readKeyCache(path, "https://other"); ok {
		t.Fatal("a cache from another endpoint must not be used")
	}
}

func TestSigningKeysForOfflineTrustsOnlyAKnownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	now := time.Now()
	down := func(context.Context) ([]skillshare.SigningKey, error) { return nil, errors.New("network down") }
	_ = writeKeyCache(path, signingKeyCache{Endpoint: "https://x", FetchedAt: now.Add(-48 * time.Hour), Keys: []skillshare.SigningKey{keyOf("v1")}})
	if _, err := signingKeysFor(context.Background(), path, "https://x", "v1", now, down); err != nil {
		t.Fatalf("a stale cache that holds the version is usable offline: %v", err)
	}
	if _, err := signingKeysFor(context.Background(), path, "https://x", "v7", now, down); err == nil {
		t.Fatal("an unknown key version must never verify from a cache")
	}
}

// registry serves one package and the signing keys, like the Gateway.
func registry(t *testing.T, priv ed25519.PrivateKey, key skillshare.SigningKey, mutate func(map[string]any)) *api.Client {
	t.Helper()
	pkg := []byte("not-a-real-tarball")
	sum := sha256.Sum256(pkg)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	payload, _ := json.Marshal(map[string]any{
		"validated": true, "artifact_digest": digest, "publisher": "dev.telara", "name": "recent-mail", "version": "0.2.0",
	})
	body := map[string]any{
		"publisher": "dev.telara", "name": "recent-mail", "version": "0.2.0", "artifact_digest": digest,
		"package_base64": base64.StdEncoding.EncodeToString(pkg),
		"attestation": map[string]any{
			"validated": true, "signing_key_version": key.Version,
			"signature":      base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
			"signed_payload": base64.StdEncoding.EncodeToString(payload),
		},
	}
	if mutate != nil {
		mutate(body)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/primitives/package":
			_ = json.NewEncoder(w).Encode(body)
		case "/v1/cli/primitives/signing-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []skillshare.SigningKey{key}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return api.NewClient(srv.URL, "tlrc_test")
}

func TestFetchPrimitiveVerifiesTheAttestationEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key := skillshare.SigningKey{KeyID: "tap-attestation:v1", Version: "v1", Alg: "ed25519", PublicKeyB64: base64.StdEncoding.EncodeToString(pub)}

	in, err := fetchPrimitive(context.Background(), registry(t, priv, key, nil), "dev.telara/recent-mail@0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if in.Attestation == nil || in.Attestation.KeyID != "tap-attestation:v1" {
		t.Fatalf("attestation %+v", in.Attestation)
	}
}

func TestFetchPrimitiveRefuses(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	wrongPub, _, _ := ed25519.GenerateKey(rand.Reader)
	key := skillshare.SigningKey{KeyID: "tap-attestation:v1", Version: "v1", Alg: "ed25519", PublicKeyB64: base64.StdEncoding.EncodeToString(pub)}
	wrongKey := key
	wrongKey.PublicKeyB64 = base64.StdEncoding.EncodeToString(wrongPub)

	cases := []struct {
		name   string
		key    skillshare.SigningKey
		mutate func(map[string]any)
		want   string
	}{
		{"no attestation", key, func(b map[string]any) { delete(b, "attestation") }, "unsigned"},
		{"tampered attestation", key, func(b map[string]any) {
			a := b["attestation"].(map[string]any)
			a["signed_payload"] = base64.StdEncoding.EncodeToString([]byte(`{"validated":true,"publisher":"dev.evil"}`))
		}, "does not verify"},
		{"tampered package", key, func(b map[string]any) {
			b["package_base64"] = base64.StdEncoding.EncodeToString([]byte("swapped"))
		}, "refusing to use them"},
		{"wrong key", wrongKey, nil, "does not verify"},
		{"unknown key version", key, func(b map[string]any) {
			b["attestation"].(map[string]any)["signing_key_version"] = "v9"
		}, "v9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			_, err := fetchPrimitive(context.Background(), registry(t, priv, c.key, c.mutate), "dev.telara/recent-mail@0.2.0")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to name %q", err, c.want)
			}
		})
	}
}
