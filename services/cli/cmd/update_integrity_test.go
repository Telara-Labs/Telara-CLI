package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/version"
)

// updateRelease serves one release the way GitHub does. checksum is the line
// body of telara_checksums.txt for the archive, or "" to omit the file.
func updateRelease(t *testing.T, tag string, archive []byte, checksum func(filename string) string) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: tag})
	}))
	t.Cleanup(api.Close)
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "telara_checksums.txt"):
			if checksum == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprint(w, checksum(buildFilename(tag)))
		default:
			_, _ = w.Write(archive)
		}
	}))
	t.Cleanup(dl.Close)
	origAPI, origDL := githubAPILatestURL, githubReleaseDownloadURL
	githubAPILatestURL, githubReleaseDownloadURL = api.URL, dl.URL
	t.Cleanup(func() { githubAPILatestURL, githubReleaseDownloadURL = origAPI, origDL })
}

func installedCopy(t *testing.T, current string) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, binaryName())
	original := []byte("installed-binary-original")
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatal(err)
	}
	origExec, origVersion := executablePath, version.Version
	executablePath = func() (string, error) { return path, nil }
	version.Version = current
	t.Cleanup(func() { executablePath, version.Version = origExec, origVersion })
	return path, original
}

func sum(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func TestUpdateRefusesAnArchiveWhoseChecksumDoesNotMatch(t *testing.T) {
	archive := makeFakeTarGz(t)
	updateRelease(t, "v3.0.0", archive, func(name string) string {
		return sum([]byte("some other archive")) + "  " + name + "\n"
	})
	path, original := installedCopy(t, "1.0.0")

	if err := runUpdate(updateCmd, nil); err == nil {
		t.Fatal("update installed an archive whose checksum does not match")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("installed binary changed: %q", got)
	}
}

func TestUpdateRefusesARelease_WithoutAChecksumsFile(t *testing.T) {
	updateRelease(t, "v3.0.0", makeFakeTarGz(t), nil)
	path, original := installedCopy(t, "1.0.0")

	if err := runUpdate(updateCmd, nil); err == nil {
		t.Fatal("update installed an archive it could not verify")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("installed binary changed: %q", got)
	}
}

func TestUpdateInstallsAnArchiveWhoseChecksumMatches(t *testing.T) {
	archive := makeFakeTarGz(t)
	updateRelease(t, "v3.0.0", archive, func(name string) string {
		return sum([]byte("unrelated")) + "  other_file.tar.gz\n" + sum(archive) + "  " + name + "\n"
	})
	path, original := installedCopy(t, "1.0.0")

	if err := runUpdate(updateCmd, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) == string(original) {
		t.Fatal("a verified update was not installed")
	}
}

func TestUpdateDoesNotInstallAnOlderLatestVersion(t *testing.T) {
	archive := makeFakeTarGz(t)
	updateRelease(t, "v0.9.0", archive, func(name string) string { return sum(archive) + "  " + name + "\n" })
	path, original := installedCopy(t, "1.2.0")

	_ = runUpdate(updateCmd, nil)
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("an older release replaced the installed binary: %q", got)
	}
}

func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"1.10.0", "1.9.0", true},
		{"1.9.0", "1.10.0", false},
		{"1.2.0", "1.2.0", false},
		{"0.9.0", "1.2.0", false},
		{"2.0.0", "dev", true},
	} {
		got, err := versionNewer(c.latest, c.current)
		if err != nil || got != c.want {
			t.Errorf("versionNewer(%q,%q) = %v, %v; want %v", c.latest, c.current, got, err, c.want)
		}
	}
	if _, err := versionNewer("nightly", "1.0.0"); err == nil {
		t.Error("an unparseable latest version must be an error")
	}
}
