package api

import (
	"context"
	"net/url"
)

// TAP primitives (TENG-2962). Only what the tenant's promotion gate has
// promoted is listed or delivered; the gate is agent-service's, reached
// through the gateway's /v1/cli/primitives routes.

// PromotedPrimitive is one entry of the promoted list, at its latest promoted
// version.
type PromotedPrimitive struct {
	Publisher string `json:"publisher"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
}

// PrimitivePackage is one promoted version and its package (gzip tar).
type PrimitivePackage struct {
	Publisher      string `json:"publisher"`
	Name           string `json:"name"`
	Version        string `json:"version"`
	ArtifactDigest string `json:"artifact_digest"`
	Sealed         bool   `json:"sealed"`
	ManifestJSON   string `json:"manifest_json"`
	// PackageBase64 is the package; decoding it is the installer's job, so the
	// digest is checked against exactly the bytes that are written.
	PackageBase64 string `json:"package_base64"`
	// Attestation is the publish attestation the registry signed. Nil when the
	// registry sent none, which the installer treats as unverifiable.
	Attestation *PrimitiveAttestation `json:"attestation,omitempty"`
}

// PrimitiveAttestation is the signed publish attestation. SignedPayload is
// base64 of the exact bytes the signature covers; Signature is base64 of the
// raw Ed25519 signature; SigningKeyVersion names the published key that made it.
type PrimitiveAttestation struct {
	Validated         bool   `json:"validated"`
	ValidatorVersion  string `json:"validator_version"`
	ScanStatus        string `json:"scan_status"`
	Signature         string `json:"signature"`
	SigningKeyVersion string `json:"signing_key_version"`
	SignedAt          string `json:"signed_at"`
	SignedPayload     string `json:"signed_payload"`
}

// PrimitiveSigningKey is one version of the key that signs attestations.
type PrimitiveSigningKey struct {
	KeyID        string `json:"key_id"`
	Version      string `json:"version"`
	Alg          string `json:"alg"`
	PublicKeyB64 string `json:"public_key_b64"`
}

// GetPrimitiveSigningKeys returns every published version of the attestation
// signing key.
func (c *Client) GetPrimitiveSigningKeys(ctx context.Context) ([]PrimitiveSigningKey, error) {
	var resp struct {
		Keys []PrimitiveSigningKey `json:"keys"`
	}
	if err := c.do(ctx, "GET", "/v1/cli/primitives/signing-keys", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Keys, nil
}

// ListPromotedPrimitives returns the primitives this tenant distributes.
func (c *Client) ListPromotedPrimitives(ctx context.Context) ([]PromotedPrimitive, error) {
	var resp struct {
		Primitives []PromotedPrimitive `json:"primitives"`
	}
	if err := c.do(ctx, "GET", "/v1/cli/primitives", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Primitives, nil
}

// GetPrimitivePackage fetches a promoted version's package. ref is
// publisher/name or publisher/name@version; without a version it is the latest
// promoted one.
func (c *Client) GetPrimitivePackage(ctx context.Context, ref string) (*PrimitivePackage, error) {
	var resp PrimitivePackage
	if err := c.do(ctx, "GET", "/v1/cli/primitives/package?ref="+url.QueryEscape(ref), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
