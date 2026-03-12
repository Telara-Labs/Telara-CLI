package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

// withTestURLs overrides the package-level URL vars, runs fn, then restores them.
func withTestURLs(cdnBase, ghAPI, ghDownload string, fn func()) {
	origCDN := primaryBaseURL
	origAPI := githubAPILatestURL
	origDL := githubReleaseDownloadURL
	primaryBaseURL = cdnBase
	githubAPILatestURL = ghAPI
	githubReleaseDownloadURL = ghDownload
	defer func() {
		primaryBaseURL = origCDN
		githubAPILatestURL = origAPI
		githubReleaseDownloadURL = origDL
	}()
	fn()
}

// makeFakeTarGz creates a valid .tar.gz containing a fake binary named "telara".
func makeFakeTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	bname := "telara"
	if runtime.GOOS == "windows" {
		bname = "telara.exe"
	}
	content := []byte("#!/bin/sh\necho fake-binary\n")

	if err := tw.WriteHeader(&tar.Header{
		Name: bname,
		Size: int64(len(content)),
		Mode: 0755,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestFetchLatestVersion_CDNSuccess(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest-version" {
			fmt.Fprint(w, "1.2.3\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer cdn.Close()

	withTestURLs(cdn.URL, "http://should-not-be-called", "http://should-not-be-called", func() {
		ver, err := fetchLatestVersion()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "1.2.3" {
			t.Fatalf("expected 1.2.3, got %s", ver)
		}
	})
}

func TestFetchLatestVersion_CDNFails_GitHubFallback(t *testing.T) {
	cdn := httptest.NewServer(http.NotFoundHandler())
	defer cdn.Close()

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(githubRelease{TagName: "v2.0.0"})
	}))
	defer gh.Close()

	withTestURLs(cdn.URL, gh.URL, "http://unused", func() {
		ver, err := fetchLatestVersion()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "2.0.0" {
			t.Fatalf("expected 2.0.0, got %s", ver)
		}
	})
}

func TestFetchLatestVersion_BothFail(t *testing.T) {
	cdn := httptest.NewServer(http.NotFoundHandler())
	defer cdn.Close()
	gh := httptest.NewServer(http.NotFoundHandler())
	defer gh.Close()

	withTestURLs(cdn.URL, gh.URL, "http://unused", func() {
		_, err := fetchLatestVersion()
		if err == nil {
			t.Fatal("expected error when both sources fail")
		}
	})
}

func TestBuildFilename_StripsVPrefix(t *testing.T) {
	f1 := buildFilename("v1.0.0")
	f2 := buildFilename("1.0.0")
	if f1 != f2 {
		t.Fatalf("v-prefix should be stripped: %s vs %s", f1, f2)
	}
	if f1 == "" {
		t.Fatal("filename should not be empty")
	}
	// Should not contain "v1.0.0"
	expected := fmt.Sprintf("telara_1.0.0_%s_%s.", runtime.GOOS, runtime.GOARCH)
	if !bytes.HasPrefix([]byte(f1), []byte(expected)) {
		t.Fatalf("expected prefix %s, got %s", expected, f1)
	}
}

func TestDownloadFile_FallbackToGitHub(t *testing.T) {
	archiveBytes := makeFakeTarGz(t)

	cdn := httptest.NewServer(http.NotFoundHandler())
	defer cdn.Close()

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve the archive for any request
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(archiveBytes)
	}))
	defer gh.Close()

	tmpDir := t.TempDir()
	dest := tmpDir + "/test.tar.gz"

	// Primary should fail
	err := downloadFile(cdn.URL+"/download/1.0.0/telara_1.0.0_test.tar.gz", dest)
	if err == nil {
		t.Fatal("expected CDN download to fail")
	}

	// Fallback should succeed
	err = downloadFile(gh.URL+"/v1.0.0/telara_1.0.0_test.tar.gz", dest)
	if err != nil {
		t.Fatalf("GitHub download should succeed: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("downloaded file should exist: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("downloaded file should not be empty")
	}
}

func TestExtractBinary_TarGz(t *testing.T) {
	archiveBytes := makeFakeTarGz(t)
	tmpDir := t.TempDir()

	archivePath := tmpDir + "/test.tar.gz"
	if err := os.WriteFile(archivePath, archiveBytes, 0644); err != nil {
		t.Fatal(err)
	}

	binaryPath := tmpDir + "/" + binaryName()
	if err := extractBinary(archivePath, binaryPath); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("extracted binary should not be empty")
	}
}

func TestFullUpdateFlow_CDNDown_GitHubWorks(t *testing.T) {
	archiveBytes := makeFakeTarGz(t)

	// CDN: 404 for everything
	cdn := httptest.NewServer(http.NotFoundHandler())
	defer cdn.Close()

	// GitHub API: returns version
	ghAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(githubRelease{TagName: "v3.0.0"})
	}))
	defer ghAPI.Close()

	// GitHub download: serves archive
	ghDL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/v3.0.0/telara_3.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
		if runtime.GOOS == "windows" {
			expectedPath = fmt.Sprintf("/v3.0.0/telara_3.0.0_%s_%s.zip", runtime.GOOS, runtime.GOARCH)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("unexpected download path: %s (expected %s)", r.URL.Path, expectedPath)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(archiveBytes)
	}))
	defer ghDL.Close()

	withTestURLs(cdn.URL, ghAPI.URL, ghDL.URL, func() {
		// Verify version resolution
		ver, err := fetchLatestVersion()
		if err != nil {
			t.Fatalf("fetchLatestVersion: %v", err)
		}
		if ver != "3.0.0" {
			t.Fatalf("expected 3.0.0, got %s", ver)
		}

		// Verify filename generation
		filename := buildFilename(ver)
		ext := "tar.gz"
		if runtime.GOOS == "windows" {
			ext = "zip"
		}
		expected := fmt.Sprintf("telara_3.0.0_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)
		if filename != expected {
			t.Fatalf("expected filename %s, got %s", expected, filename)
		}

		// Verify download fallback works
		tmpDir := t.TempDir()
		archivePath := tmpDir + "/" + filename

		// CDN should fail
		primaryURL := fmt.Sprintf("%s/download/%s/%s", primaryBaseURL, ver, filename)
		err = downloadFile(primaryURL, archivePath)
		if err == nil {
			t.Fatal("CDN download should have failed")
		}

		// GitHub should succeed
		fallbackURL := fmt.Sprintf("%s/v%s/%s", githubReleaseDownloadURL, ver, filename)
		err = downloadFile(fallbackURL, archivePath)
		if err != nil {
			t.Fatalf("GitHub download failed: %v", err)
		}

		// Verify extraction
		binaryPath := tmpDir + "/" + binaryName()
		err = extractBinary(archivePath, binaryPath)
		if err != nil {
			t.Fatalf("extract failed: %v", err)
		}
	})
}
