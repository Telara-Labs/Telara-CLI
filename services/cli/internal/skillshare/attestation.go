package skillshare

// attestation.go checks the publish attestation that arrives with a package
// before anything is installed, and writes it beside the installed package so
// the runner can check it again offline.
//
// What is checked, in order, and what each failure means:
//
//	signature   the registry's Ed25519 signature over the exact signed bytes,
//	            using the published key version that signed it
//	identity    the signed publisher, name, version and artifact_digest equal
//	            the package that was delivered
//	validated   the publish pipeline passed
//
// The signature is verified over the bytes as received. They are parsed only
// after the signature holds, and never re-marshalled.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Files written beside an installed package. ATTESTATION.json holds the signed
// bytes verbatim; ATTESTATION.sig names the key and carries the signature.
const (
	AttestationFile = "ATTESTATION.json"
	SignatureFile   = "ATTESTATION.sig"
)

// SigningKey is one published version of the attestation signing key.
type SigningKey struct {
	KeyID        string `json:"key_id"`
	Version      string `json:"version"`
	Alg          string `json:"alg"`
	PublicKeyB64 string `json:"public_key_b64"`
}

// SignedAttestation is an attestation as delivered: signed bytes (base64),
// the raw signature (base64) and the key version that made it.
type SignedAttestation struct {
	SignedPayloadB64  string
	SignatureB64      string
	SigningKeyVersion string
}

// VerifiedAttestation is an attestation whose signature and identity held.
type VerifiedAttestation struct {
	Payload   []byte // the signed bytes, written as ATTESTATION.json
	Signature []byte // written inside ATTESTATION.sig
	KeyID     string
	Alg       string
}

// signatureFile is the content of ATTESTATION.sig.
type signatureFile struct {
	KeyID     string `json:"key_id"`
	Alg       string `json:"alg"`
	Signature string `json:"signature"`
}

// signedFields is the part of the signed bytes the installer checks.
type signedFields struct {
	Validated      bool   `json:"validated"`
	ArtifactDigest string `json:"artifact_digest"`
	Publisher      string `json:"publisher"`
	Name           string `json:"name"`
	Version        string `json:"version"`
}

// VerifyAttestation checks att against the delivered package p using the
// published keys. Every failure names what failed.
func VerifyAttestation(p PrimitiveInstall, att *SignedAttestation, keys []SigningKey) (*VerifiedAttestation, error) {
	if att == nil || att.SignatureB64 == "" || att.SignedPayloadB64 == "" {
		return nil, fmt.Errorf("%s: the registry sent no signed attestation; refusing to install an unsigned package", p.Ref())
	}
	payload, err := base64.StdEncoding.DecodeString(att.SignedPayloadB64)
	if err != nil || len(payload) == 0 {
		return nil, fmt.Errorf("%s: attestation payload is not valid base64", p.Ref())
	}
	sig, err := base64.StdEncoding.DecodeString(att.SignatureB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%s: attestation signature is not a valid Ed25519 signature", p.Ref())
	}
	var key *SigningKey
	for i := range keys {
		if keys[i].Version == att.SigningKeyVersion {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return nil, fmt.Errorf("%s: attestation was signed by key version %q, which the registry does not publish", p.Ref(), att.SigningKeyVersion)
	}
	if key.Alg != "ed25519" {
		return nil, fmt.Errorf("%s: signing key %s uses unsupported algorithm %q", p.Ref(), key.KeyID, key.Alg)
	}
	pub, err := base64.StdEncoding.DecodeString(key.PublicKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s: signing key %s is not a valid Ed25519 public key", p.Ref(), key.KeyID)
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, fmt.Errorf("%s: attestation signature does not verify against key %s", p.Ref(), key.KeyID)
	}

	var f signedFields
	if err := json.Unmarshal(payload, &f); err != nil {
		return nil, fmt.Errorf("%s: signed attestation is not valid JSON: %w", p.Ref(), err)
	}
	for _, c := range []struct{ field, signed, got string }{
		{"publisher", f.Publisher, p.Publisher},
		{"name", f.Name, p.Name},
		{"version", f.Version, p.Version},
		{"artifact_digest", f.ArtifactDigest, p.ArtifactDigest},
	} {
		if c.signed != c.got {
			return nil, fmt.Errorf("%s: attestation %s is %q but the delivered package is %q", p.Ref(), c.field, c.signed, c.got)
		}
	}
	if !f.Validated {
		return nil, fmt.Errorf("%s: attestation says the publish pipeline did not validate this package", p.Ref())
	}
	return &VerifiedAttestation{Payload: payload, Signature: sig, KeyID: key.KeyID, Alg: key.Alg}, nil
}

// writeAttestation writes ATTESTATION.json and ATTESTATION.sig into dir. A nil
// attestation writes nothing.
func writeAttestation(dir string, v *VerifiedAttestation) error {
	if v == nil {
		return nil
	}
	sig, err := json.MarshalIndent(signatureFile{
		KeyID: v.KeyID, Alg: v.Alg, Signature: base64.StdEncoding.EncodeToString(v.Signature),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, AttestationFile), v.Payload, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SignatureFile), append(sig, '\n'), 0o644)
}
