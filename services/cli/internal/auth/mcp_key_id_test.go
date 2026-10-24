package auth

import "testing"

// The cached MCP key carries the server id of the key so logout revokes
// exactly that key (key prefixes are shared by every key of a tenant).
func TestSaveMCPKeyWithIDRoundTripsAndPlainSaveClearsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const api = "https://api.telara.dev"

	if err := SaveMCPKeyWithID(api, "telara_mcp_abcd1234SECRET", "key-77"); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadMCPKey(api); err != nil || key != "telara_mcp_abcd1234SECRET" {
		t.Fatalf("key = %q, %v", key, err)
	}
	if id, err := LoadMCPKeyID(api); err != nil || id != "key-77" {
		t.Fatalf("id = %q, %v; want key-77", id, err)
	}

	// A key stored without an id replaces the old one, so the old id must not
	// stay attached to a different key.
	if err := SaveMCPKey(api, "telara_mcp_other"); err != nil {
		t.Fatal(err)
	}
	if id, err := LoadMCPKeyID(api); err != nil || id != "" {
		t.Fatalf("id after a plain save = %q, %v; want empty", id, err)
	}
}
