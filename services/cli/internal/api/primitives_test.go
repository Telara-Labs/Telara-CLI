package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The CLI and the gateway meet at these URLs; pinning them here is what
// notices a rename on either side.
func TestPrimitiveRoutes(t *testing.T) {
	var gotPath, gotRef string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRef = r.URL.Path, r.URL.Query().Get("ref")
		switch r.URL.Path {
		case "/v1/cli/primitives":
			_ = json.NewEncoder(w).Encode(map[string]any{"primitives": []map[string]string{{"ref": "dev.telara/recent-mail@0.2.0"}}})
		case "/v1/cli/primitives/package":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "recent-mail", "artifact_digest": "sha256:abc", "package_base64": "eA=="})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")

	list, err := c.ListPromotedPrimitives(context.Background())
	if err != nil || len(list) != 1 || list[0].Ref != "dev.telara/recent-mail@0.2.0" {
		t.Fatalf("list %+v %v", list, err)
	}
	pkg, err := c.GetPrimitivePackage(context.Background(), "dev.telara/recent-mail@0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/cli/primitives/package" || gotRef != "dev.telara/recent-mail@0.2.0" {
		t.Fatalf("requested %s ref=%q", gotPath, gotRef)
	}
	if pkg.ArtifactDigest != "sha256:abc" || pkg.PackageBase64 != "eA==" {
		t.Fatalf("pkg %+v", pkg)
	}
}
