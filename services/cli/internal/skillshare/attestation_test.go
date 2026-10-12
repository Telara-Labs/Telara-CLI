package skillshare

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signer plays the registry: it holds a private key generated for the test.
type signer struct {
	priv ed25519.PrivateKey
	key  SigningKey
}

func newSigner(t *testing.T, version string) signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{priv: priv, key: SigningKey{
		KeyID: "tap-attestation:" + version, Version: version, Alg: "ed25519",
		PublicKeyB64: base64.StdEncoding.EncodeToString(pub),
	}}
}

func (s signer) attest(payload string) *SignedAttestation {
	return &SignedAttestation{
		SignedPayloadB64:  base64.StdEncoding.EncodeToString([]byte(payload)),
		SignatureB64:      base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, []byte(payload))),
		SigningKeyVersion: s.key.Version,
	}
}

func payloadFor(p PrimitiveInstall, validated bool) string {
	b, _ := json.Marshal(map[string]any{
		"validated": validated, "artifact_digest": p.ArtifactDigest,
		"publisher": p.Publisher, "name": p.Name, "version": p.Version,
	})
	return string(b)
}

func TestVerifyAttestationAcceptsAGenuineOne(t *testing.T) {
	p := install(goodPackage(t))
	s := newSigner(t, "v1")
	v, err := VerifyAttestation(p, s.attest(payloadFor(p, true)), []SigningKey{s.key})
	if err != nil {
		t.Fatal(err)
	}
	if v.KeyID != "tap-attestation:v1" || string(v.Payload) != payloadFor(p, true) {
		t.Fatalf("verified %+v", v)
	}
}

func TestVerifyAttestationRefuses(t *testing.T) {
	p := install(goodPackage(t))
	s := newSigner(t, "v1")
	other := newSigner(t, "v1")
	good := s.attest(payloadFor(p, true))

	tamperedPayload := *good
	tamperedPayload.SignedPayloadB64 = base64.StdEncoding.EncodeToString([]byte(strings.Replace(payloadFor(p, true), `"validated":true`, `"validated":true `, 1)))

	otherPkg := install(packageOf(t, entry{name: "primitive.yaml", body: manifestYAML + "# changed\n"}))
	unknownVersion := s.attest(payloadFor(p, true))
	unknownVersion.SigningKeyVersion = "v9"

	cases := []struct {
		name string
		pkg  PrimitiveInstall
		att  *SignedAttestation
		keys []SigningKey
		want string
	}{
		{"no attestation", p, nil, []SigningKey{s.key}, "unsigned"},
		{"tampered attestation", p, &tamperedPayload, []SigningKey{s.key}, "does not verify"},
		{"tampered package", otherPkg, good, []SigningKey{s.key}, "artifact_digest"},
		{"wrong key", p, good, []SigningKey{other.key}, "does not verify"},
		{"unknown key version", p, unknownVersion, []SigningKey{s.key}, "does not publish"},
		{"not validated", p, s.attest(payloadFor(p, false)), []SigningKey{s.key}, "did not validate"},
		{"signed for another version", p, s.attest(strings.Replace(payloadFor(p, true), `"version":"0.2.0"`, `"version":"0.1.0"`, 1)), []SigningKey{s.key}, "version"},
		{"signed for another publisher", p, s.attest(strings.Replace(payloadFor(p, true), "dev.telara", "dev.evil", 1)), []SigningKey{s.key}, "publisher"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := VerifyAttestation(c.pkg, c.att, c.keys)
			if err == nil || v != nil {
				t.Fatalf("accepted: %+v", v)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

func TestInstallPrimitiveWritesAttestationFilesTheRunnerReads(t *testing.T) {
	root := t.TempDir()
	p := install(goodPackage(t))
	s := newSigner(t, "v2")
	v, err := VerifyAttestation(p, s.attest(payloadFor(p, true)), []SigningKey{s.key})
	if err != nil {
		t.Fatal(err)
	}
	p.Attestation = v
	if _, err := InstallPrimitive(root, p, false); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "recent-mail")
	payload, err := os.ReadFile(filepath.Join(dir, AttestationFile))
	if err != nil || string(payload) != payloadFor(p, true) {
		t.Fatalf("ATTESTATION.json %q: %v", payload, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, SignatureFile))
	if err != nil {
		t.Fatal(err)
	}
	var sf signatureFile
	if err := json.Unmarshal(raw, &sf); err != nil || sf.KeyID != "tap-attestation:v2" || sf.Alg != "ed25519" {
		t.Fatalf("ATTESTATION.sig %s: %+v %v", raw, sf, err)
	}
	sig, _ := base64.StdEncoding.DecodeString(sf.Signature)
	pub, _ := base64.StdEncoding.DecodeString(s.key.PublicKeyB64)
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("the files written beside the package must verify on their own")
	}
}

func TestInstallPrimitiveWithoutAttestationWritesNone(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallPrimitive(root, install(goodPackage(t)), false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{AttestationFile, SignatureFile} {
		if _, err := os.Stat(filepath.Join(root, "recent-mail", f)); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist: %v", f, err)
		}
	}
}
