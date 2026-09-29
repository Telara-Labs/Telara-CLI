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
