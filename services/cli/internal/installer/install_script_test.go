package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var publicInstaller = flag.Bool("installer-public", false, "check current public CLI installer in an owned temporary destination")

// This is an installer control-flow test with real shells and curl, against
// generic owned HTTP fixtures. It does not simulate or verify Telara services.
func TestInstallScriptVersionFallback(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Unix installer")
	}
	for _, shell := range []struct {
		name string
		args []string
	}{
		{"bash", nil}, {"sh", nil}, {"dash", nil}, {"busybox", []string{"sh"}},
	} {
		t.Run(shell.name, func(t *testing.T) {
			path, err := exec.LookPath(shell.name)
			if err != nil {
				t.Skip(err)
			}
			for _, tc := range []struct {
				name        string
				status      int
				body        string
				disconnect  bool
				githubError bool
				githubEmpty bool
				githubNoTag bool
				fallback    bool
			}{
				{name: "HTTP403", status: 403, fallback: true},
				{name: "transport failure", disconnect: true, fallback: true},
				{name: "empty response", status: 200, fallback: true},
				{name: "primary success", status: 200, body: "v0.0.1"},
				{name: "both version endpoints fail", status: 403, githubError: true, fallback: true},
				{name: "GitHub empty response", status: 403, githubEmpty: true, fallback: true},
				{name: "GitHub missing tag", status: 403, githubNoTag: true, fallback: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					archive := fixtureArchive(t)
					var mu sync.Mutex
					var requests []string
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						requests = append(requests, r.URL.Path)
						mu.Unlock()
						switch r.URL.Path {
						case "/primary/latest-version":
							if tc.disconnect {
								conn, _, err := w.(http.Hijacker).Hijack()
								if err == nil {
									conn.Close()
								}
								return
							}
							w.WriteHeader(tc.status)
							fmt.Fprint(w, tc.body)
						case "/github/latest":
							if tc.githubError {
								w.WriteHeader(403)
								return
							}
							if tc.githubEmpty {
								return
							}
							if tc.githubNoTag {
								fmt.Fprint(w, `{"name":"no release tag"}`)
								return
							}
							fmt.Fprint(w, `{"tag_name":"v0.0.1"}`)
						default:
							if strings.HasPrefix(r.URL.Path, "/primary/download/") && tc.fallback {
								w.WriteHeader(403)
								return
							}
							w.Write(archive)
						}
					}))
					defer server.Close()
					script, err := os.ReadFile("../../../../scripts/install.sh")
					if err != nil {
						t.Fatal(err)
					}
					// Substitute only the existing constant endpoints in an owned copy.
					body := string(script)
					for from, to := range map[string]string{
						`PRIMARY_BASE_URL="https://get.telara.dev"`:                             `PRIMARY_BASE_URL="` + server.URL + `/primary"`,
						`GITHUB_API_URL="https://api.github.com/repos/${REPO}/releases/latest"`: `GITHUB_API_URL="` + server.URL + `/github/latest"`,
						`GITHUB_DOWNLOAD_URL="https://github.com/${REPO}/releases/download"`:    `GITHUB_DOWNLOAD_URL="` + server.URL + `/github/download"`,
					} {
						if strings.Count(body, from) != 1 {
							t.Fatalf("installer endpoint constant changed: %s", from)
						}
						body = strings.Replace(body, from, to, 1)
					}
					install := t.TempDir()
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, path, shell.args...)
					cmd.Stdin = strings.NewReader(body)
					for _, e := range os.Environ() {
						if !strings.HasPrefix(e, "TELARA_INSTALL_DIR=") && !strings.HasPrefix(e, "TELARA_VERSION=") {
							cmd.Env = append(cmd.Env, e)
						}
					}
					cmd.Env = append(cmd.Env, "TELARA_INSTALL_DIR="+install)
					out, runErr := cmd.CombinedOutput()
					mu.Lock()
					got := append([]string{}, requests...)
					mu.Unlock()
					t.Logf("actual %s %v/curl: %v; requests %v\n%s", shell.name, shell.args, runErr, got, out)
					github := false
					for _, request := range got {
						github = github || request == "/github/latest"
					}
					if github != tc.fallback {
						t.Errorf("GitHub version lookup = %t, want %t", github, tc.fallback)
					}
					binary := filepath.Join(install, "telara")
					if tc.githubError || tc.githubEmpty || tc.githubNoTag {
						if runErr == nil {
							t.Error("both version lookups failed but installer succeeded")
						}
						if _, err := os.Stat(binary); !os.IsNotExist(err) {
							t.Errorf("failed lookup installed a binary: %v", err)
						}
						return
					}
					if runErr != nil {
						t.Fatalf("installer failed: %v\n%s", runErr, out)
					}
					probe, err := exec.Command(binary, "-test.run=^TestInstallerBinaryFixture$", "-test.v").Output()
					if err != nil || !strings.Contains(string(probe), "owned executable fixture") {
						t.Fatalf("installed executable: %q, %v", probe, err)
					}
				})
			}
		})
	}
}

func fixtureArchive(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	if err := w.WriteHeader(&tar.Header{Name: "telara", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInstallerBinaryFixture(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(self) != "telara" {
		t.Skip("owned installed test executable only")
	}
	fmt.Println("owned executable fixture")
}

// An explicit live check is separate from the generic status regressions.
// It checks serving bytes, then runs reviewed local source against real public
// metadata/download endpoints. It does not establish serving deployment.
func TestPublicInstaller(t *testing.T) {
	if !*publicInstaller || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("pass -installer-public for current public Unix installer acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fetch := func(url string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "curl", "-fsSL", "--max-time", "20", url).Output()
		if err != nil {
			t.Fatalf("public download %s: %v", url, err)
		}
		return out
	}
	version := strings.TrimSpace(string(fetch("https://get.telara.dev/latest-version")))
	script := fetch("https://get.telara.dev/install.sh")
	local, err := os.ReadFile("../../../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Exact previously reviewed public v0.1.44 script, retained until serving
	// deployment catches up. An unexpected public edit requires fresh review.
	const reviewedOriginal = "2cb4e86729bab2ffcfa283045f06976fdef816110709279ae2bafa58a0bd1c81"
	if !bytes.Equal(script, local) && fmt.Sprintf("%x", sha256.Sum256(script)) != reviewedOriginal {
		t.Fatal("public installer differs from reviewed local/current pre-fix script; inspect before execution")
	}
	install := t.TempDir()
	cmd := exec.CommandContext(ctx, "sh")
	cmd.Stdin = bytes.NewReader(local)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TELARA_INSTALL_DIR=") && !strings.HasPrefix(e, "TELARA_VERSION=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "TELARA_INSTALL_DIR="+install, "HOME="+install)
	out, err := cmd.CombinedOutput()
	t.Logf("public installer version %s, served script SHA256 %x, matches local=%t; actual sh with local source SHA256 %x\n%s", version, sha256.Sum256(script), bytes.Equal(script, local), sha256.Sum256(local), out)
	if err != nil {
		t.Fatalf("public installer: %v", err)
	}
	binary := filepath.Join(install, "telara")
	versionCommand := exec.CommandContext(ctx, binary, "version")
	versionCommand.Env = cmd.Env
	probe, err := versionCommand.CombinedOutput()
	if err != nil || !strings.Contains(string(probe), strings.TrimPrefix(version, "v")) {
		t.Fatalf("downloaded public CLI version: %q, %v", probe, err)
	}
	b, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("public installed CLI SHA256 %x; %s", sha256.Sum256(b), probe)
}
