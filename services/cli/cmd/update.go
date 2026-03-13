package cmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/display"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/version"
)

// URL variables for the update sources. Tests override these to use httptest servers.
var (
	githubAPILatestURL       = "https://api.github.com/repos/Telera-Labs/Telara-CLI/releases/latest"
	githubReleaseDownloadURL = "https://github.com/Telara-Labs/Telara-CLI/releases/download"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update telara CLI to the latest version",
	RunE:  runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	spinner := display.NewSpinner()
	spinner.Start("Checking for latest version")
	latest, err := fetchLatestVersion()
	if err != nil {
		spinner.Fail("Version check failed")
		return fmt.Errorf("failed to fetch latest version: %w", err)
	}
	spinner.Stop()

	current := version.Version
	if current == latest || (current == "dev" && latest == "") || latest == current {
		display.PrintSuccess(fmt.Sprintf("Already on latest version (%s)", current))
		return nil
	}

	display.PrintInfo(fmt.Sprintf("Updating from %s to %s", current, latest))

	filename := buildFilename(latest)

	tag := latest
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	downloadURL := fmt.Sprintf("%s/%s/%s", githubReleaseDownloadURL, tag, filename)

	tmpDir, err := os.MkdirTemp("", "telara-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, filename)
	spinner.Start("Downloading update")
	if err := downloadFile(downloadURL, archivePath); err != nil {
		spinner.Fail("Download failed")
		return fmt.Errorf("failed to download update: %w", err)
	}
	spinner.Success("Downloaded")

	newBinaryPath := filepath.Join(tmpDir, binaryName())
	if err := extractBinary(archivePath, newBinaryPath); err != nil {
		return fmt.Errorf("failed to extract binary: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine executable path: %w", err)
	}

	backupPath := execPath + ".bak"
	if err := os.Rename(execPath, backupPath); err != nil {
		printInstallInstructions(latest)
		return fmt.Errorf("cannot replace binary (permission denied) — see instructions above")
	}

	if err := os.Rename(newBinaryPath, execPath); err != nil {
		// Attempt to restore backup.
		_ = os.Rename(backupPath, execPath)
		return fmt.Errorf("failed to install new binary: %w", err)
	}

	// Remove backup on success.
	_ = os.Remove(backupPath)

	display.PrintSuccess(fmt.Sprintf("Updated to %s", latest))
	return nil
}

// buildFilename returns the archive filename for a given version.
// GoReleaser uses {{ .Version }} (without v prefix) in the name template,
// so we strip the leading "v" if present.
func buildFilename(ver string) string {
	ver = strings.TrimPrefix(ver, "v")
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("telara_%s_%s_%s.%s", ver, goos, goarch, ext)
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "telara.exe"
	}
	return "telara"
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d for %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("cannot create file %s: %w", dest, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("download interrupted: %w", err)
	}
	return nil
}

func extractBinary(archivePath, destPath string) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractFromZip(archivePath, destPath)
	}
	return extractFromTarGz(archivePath, destPath)
}

func extractFromTarGz(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	bname := binaryName()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(hdr.Name) == bname {
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("create binary: %w", err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("extract binary: %w", err)
			}
			out.Close()
			return nil
		}
	}
	return fmt.Errorf("binary %q not found in archive", bname)
}

func extractFromZip(archivePath, destPath string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	bname := binaryName()
	for _, f := range r.File {
		if filepath.Base(f.Name) == bname {
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("open zip entry: %w", err)
			}
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				rc.Close()
				return fmt.Errorf("create binary: %w", err)
			}
			_, copyErr := io.Copy(out, rc)
			rc.Close()
			out.Close()
			if copyErr != nil {
				return fmt.Errorf("extract binary: %w", copyErr)
			}
			return nil
		}
	}
	return fmt.Errorf("binary %q not found in zip archive", bname)
}

func printInstallInstructions(ver string) {
	fmt.Fprintln(os.Stderr, "Cannot replace binary due to insufficient permissions.")
	fmt.Fprintln(os.Stderr, "To upgrade, use one of the following:")
	fmt.Fprintln(os.Stderr)
	switch runtime.GOOS {
	case "darwin":
		fmt.Fprintln(os.Stderr, "  Homebrew:     brew upgrade telara")
		fmt.Fprintf(os.Stderr, "  Install script: curl -sf https://get.telara.dev/install.sh | sh\n")
	case "windows":
		fmt.Fprintln(os.Stderr, "  Re-run the PowerShell installer:")
		fmt.Fprintln(os.Stderr, "    iwr https://get.telara.dev/install.ps1 | iex")
	default:
		fmt.Fprintf(os.Stderr, "  Install script: curl -sf https://get.telara.dev/install.sh | sh\n")
	}
}

func fetchLatestVersion() (string, error) {
	return fetchLatestVersionFromGitHub()
}

// githubRelease is the minimal structure needed from the GitHub Releases API.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

func fetchLatestVersionFromGitHub() (string, error) {
	resp, err := http.Get(githubAPILatestURL) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("GitHub API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to parse GitHub API response: %w", err)
	}

	tag := strings.TrimSpace(release.TagName)
	if tag == "" {
		return "", fmt.Errorf("GitHub API returned empty tag_name")
	}

	// Strip the "v" prefix to match the version format used elsewhere.
	return strings.TrimPrefix(tag, "v"), nil
}

// versionCacheFile is the structure stored in the version cache file.
type versionCacheFile struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

// checkVersionInBackground runs a background version check and prints a notice
// to stderr if a newer version is available. It is intentionally fire-and-forget.
func checkVersionInBackground() {
	cacheDir, err := config.CacheDir()
	if err != nil {
		return
	}
	cachePath := filepath.Join(cacheDir, "latest-version.json")

	// Read the existing cache, if any.
	var cache versionCacheFile
	if data, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(data, &cache)
	}

	// Fetch a new version only if the cache is older than 24 hours.
	if time.Since(cache.CheckedAt) > 24*time.Hour {
		latest, err := fetchLatestVersion()
		if err != nil {
			return
		}
		cache = versionCacheFile{
			LatestVersion: latest,
			CheckedAt:     time.Now(),
		}
		if data, err := json.Marshal(cache); err == nil {
			_ = os.WriteFile(cachePath, data, 0600)
		}
	}

	if cache.LatestVersion == "" {
		return
	}

	current := version.Version
	if current != "dev" && cache.LatestVersion != current {
		fmt.Fprintf(os.Stderr, "A new version of telara is available: %s (run 'telara update' to upgrade)\n", cache.LatestVersion)
	}
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
