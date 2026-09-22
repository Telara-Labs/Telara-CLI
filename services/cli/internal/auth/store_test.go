package auth

import (
	"errors"
	"testing"
)

// These tests use a temp HOME directory to exercise the file-based fallback path.
// The OS keychain is not available in CI, so we rely on the file fallback.

func TestSaveAndLoad_FileOnly(t *testing.T) {
	// Point HOME at a temp dir so CredentialsDir resolves there.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	apiURL := "https://api.telara.dev"
	token := "tlrc_test_file_token_1234567890"

	if err := SaveToken(apiURL, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	got, err := LoadToken(apiURL)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != token {
		t.Errorf("LoadToken returned %q; want %q", got, token)
	}
}

func TestDeleteToken_Clears(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	apiURL := "https://api.telara.dev"
	token := "tlrc_delete_test_token_1234567890"

	if err := SaveToken(apiURL, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	if err := DeleteToken(apiURL); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}

	_, err := LoadToken(apiURL)
	if err == nil {
		t.Fatal("expected error after deletion, got nil")
	}
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("expected ErrNoToken, got: %v", err)
	}
}

func TestLoadToken_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	apiURL := "https://api.telara.dev"

	_, err := LoadToken(apiURL)
	if err == nil {
		t.Fatal("expected error when no token stored, got nil")
	}
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("expected ErrNoToken, got: %v", err)
	}
}

func TestSaveAndLoad_MultipleHosts(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	url1 := "https://api.telara.dev"
	url2 := "https://staging-api.telara.dev"
	token1 := "tlrc_host1_token_00000000000000"
	token2 := "tlrc_host2_token_00000000000000"

	if err := SaveToken(url1, token1); err != nil {
		t.Fatalf("SaveToken url1: %v", err)
	}
	if err := SaveToken(url2, token2); err != nil {
		t.Fatalf("SaveToken url2: %v", err)
	}

	got1, err := LoadToken(url1)
	if err != nil || got1 != token1 {
		t.Errorf("url1 token = %q, err = %v; want %q, nil", got1, err, token1)
	}

	got2, err := LoadToken(url2)
	if err != nil || got2 != token2 {
		t.Errorf("url2 token = %q, err = %v; want %q, nil", got2, err, token2)
	}
}

// The MCP key shares one 0600 file per host with the login token (TENG-3019).
// The two are written read-modify-write, so saving either must keep the other.

func TestSaveAndLoadMCPKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	apiURL := "https://api.telara.dev"
	if _, err := LoadMCPKey(apiURL); !errors.Is(err, ErrNoMCPKey) {
		t.Fatalf("LoadMCPKey with nothing stored = %v; want ErrNoMCPKey", err)
	}

	key := "telara_mcp_abc123"
	if err := SaveMCPKey(apiURL, key); err != nil {
		t.Fatalf("SaveMCPKey: %v", err)
	}
	got, err := LoadMCPKey(apiURL)
	if err != nil {
		t.Fatalf("LoadMCPKey: %v", err)
	}
	if got != key {
		t.Errorf("LoadMCPKey returned %q; want %q", got, key)
	}
}

func TestMCPKeyAndTokenDoNotOverwriteEachOther(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	apiURL := "https://api.telara.dev"
	token := "tlrc_login_token"
	key := "telara_mcp_abc123"

	// Save in each order: a blind marshal of either struct would drop the other.
	if err := SaveToken(apiURL, token); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveMCPKey(apiURL, key); err != nil {
		t.Fatalf("SaveMCPKey: %v", err)
	}
	if got, err := LoadToken(apiURL); err != nil || got != token {
		t.Fatalf("saving the MCP key dropped the login token: got %q, err %v", got, err)
	}

	if err := SaveToken(apiURL, "tlrc_rotated"); err != nil {
		t.Fatalf("SaveToken (rotate): %v", err)
	}
	if got, err := LoadMCPKey(apiURL); err != nil || got != key {
		t.Fatalf("rotating the login token dropped the MCP key: got %q, err %v", got, err)
	}
}

func TestDeleteMCPKeyLeavesTheLoginToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	apiURL := "https://api.telara.dev"
	if err := SaveToken(apiURL, "tlrc_login_token"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveMCPKey(apiURL, "telara_mcp_abc123"); err != nil {
		t.Fatalf("SaveMCPKey: %v", err)
	}
	if err := DeleteMCPKey(apiURL); err != nil {
		t.Fatalf("DeleteMCPKey: %v", err)
	}
	if _, err := LoadMCPKey(apiURL); !errors.Is(err, ErrNoMCPKey) {
		t.Fatalf("LoadMCPKey after delete = %v; want ErrNoMCPKey", err)
	}
	if got, err := LoadToken(apiURL); err != nil || got != "tlrc_login_token" {
		t.Fatalf("DeleteMCPKey removed the login token: got %q, err %v", got, err)
	}
}
