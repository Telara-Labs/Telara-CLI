package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func credPathFor(t *testing.T, apiURL string) string {
	t.Helper()
	host, err := sanitizeHost(apiURL)
	if err != nil {
		t.Fatal(err)
	}
	path, err := credFilePath(host)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Go applies the mode only when it creates a file. A credential file that
// already exists group or world readable must be tightened on the next save
// (UIX-G06).
func TestSaveTokenTightensAnExistingWorldReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	t.Setenv("HOME", t.TempDir())
	const apiURL = "https://api.telara.dev"
	path := credPathFor(t, apiURL)
	if err := os.WriteFile(path, []byte(`{"token":"tlrc_old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveToken(apiURL, "tlrc_new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("credential file mode %v is readable by group or others", info.Mode().Perm())
	}
	if got, _ := LoadToken(apiURL); got != "tlrc_new" {
		t.Fatalf("token = %q", got)
	}
}

// The write goes through a temp file and a rename, so an interrupted write
// leaves the old complete file and no stray partial file behind.
func TestSaveTokenReplacesTheFileAtomically(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const apiURL = "https://api.telara.dev"
	if err := SaveToken(apiURL, "tlrc_one"); err != nil {
		t.Fatal(err)
	}
	path := credPathFor(t, apiURL)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(apiURL, "tlrc_two"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("the credential file was rewritten in place")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries after a save, want only the credential file", len(entries))
	}
}

// A token saved for one origin is not loaded for another scheme or port
// (UIX-G08).
func TestTokenForHTTPSOriginIsNotLoadedForHTTPOrOtherPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveToken("https://h.example", "tlrc_https"); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"http://h.example", "https://h.example:8443", "http://h.example:443"} {
		if got, err := LoadToken(other); !errors.Is(err, ErrNoToken) {
			t.Errorf("LoadToken(%q) = %q, %v; want ErrNoToken", other, got, err)
		}
	}
	if got, err := LoadToken("https://h.example:443"); err != nil || got != "tlrc_https" {
		t.Errorf("explicit default port must match the https origin: %q, %v", got, err)
	}
	if got, err := LoadToken("https://h.example/"); err != nil || got != "tlrc_https" {
		t.Errorf("trailing slash must match: %q, %v", got, err)
	}
}
