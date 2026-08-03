package config

import (
	"os"
	"strings"
	"testing"
)

// The scan destination must be a compile-time constant. If it ever becomes
// resolvable from a flag, an env var, or stored prefs, two attacks open up:
// redirecting a fleet's discovery evidence to an attacker-controlled host, and
// pointing your own machine at a black hole so you vanish from inventory.
// These tests exist so that regression is loud.

func TestScanEndpointDefaultsToProduction(t *testing.T) {
	got := ScanSubmitEndpoint()
	if got != "https://api.telara.dev" {
		t.Fatalf("ScanSubmitEndpoint() = %q, want https://api.telara.dev — a release build must report to production", got)
	}
	if ScanEndpointIsOverridden() {
		t.Fatal("ScanEndpointIsOverridden() = true on an unmodified build; the override check is broken")
	}
}

func TestScanEndpointIgnoresEnvironment(t *testing.T) {
	// These are the knobs every other CLI call honours. The scan target must
	// not move for any of them.
	for _, key := range []string{"TELARA_API_URL", "TELARA_SCAN_URL", "TELARA_ENDPOINT"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "https://evil.example.com")
			if got := ScanSubmitEndpoint(); got != "https://api.telara.dev" {
				t.Fatalf("%s moved the scan destination to %q", key, got)
			}
		})
	}
}

func TestScanEndpointIsNotReadFromProcessEnvAtAll(t *testing.T) {
	// Belt and braces: even a wholesale environment wipe must not change it,
	// which proves the value is not derived from the environment.
	saved := os.Environ()
	os.Clearenv()
	defer func() {
		for _, kv := range saved {
			if i := strings.IndexByte(kv, '='); i > 0 {
				_ = os.Setenv(kv[:i], kv[i+1:])
			}
		}
	}()
	if got := ScanSubmitEndpoint(); got != "https://api.telara.dev" {
		t.Fatalf("scan destination changed to %q with an empty environment", got)
	}
}

func TestScanEndpointIsNormalized(t *testing.T) {
	// Whatever a build injects, the value handed to callers must be a clean
	// base URL — a trailing slash or doubled scheme would silently produce a
	// 404 on every scheduled scan.
	got := ScanSubmitEndpoint()
	if strings.HasSuffix(got, "/") {
		t.Errorf("endpoint %q has a trailing slash", got)
	}
	if strings.Count(got, "://") != 1 {
		t.Errorf("endpoint %q has a malformed scheme", got)
	}
}
