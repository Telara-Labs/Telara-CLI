package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// TENG-3017. Login already wired the user's always-on base configuration, but
// never recorded what it had wired. The global layer therefore read back as an
// anonymous "connected": `telara config` could not name it, `telara install`
// treated the user as having made no selection and bootstrapped again, and the
// interactive picker had nothing to default to. These tests pin the recording
// and the identity rule it depends on.

func isolateConfigHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
}

func TestRecordWiredGlobalMarksABaseBindingAsTheDefault(t *testing.T) {
	isolateConfigHome(t)

	recordWiredGlobal(onboardingBinding{
		RawKey:     "telara_mcp_base",
		ConfigID:   "4e0acd6f-eb68-483e-a64c-e67490302aa1",
		ConfigName: "Personal (034c5aa3-d3d0-476b-8fe3-1ea1fa93ae06)",
		IsBase:     true,
	})

	state, err := agent.LoadWiredState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Global == nil {
		t.Fatal("login auto-wire did not record the global layer")
	}
	if state.Global.ConfigID != "4e0acd6f-eb68-483e-a64c-e67490302aa1" {
		t.Fatalf("recorded the wrong config id: %q", state.Global.ConfigID)
	}
	if !state.Global.IsDefault() {
		t.Fatal("a base binding must be recorded as the user's default, not as a choice they made")
	}
}

// A downgrade is not the user's base. Recording the tenant master as their
// "default" would present the union of every policy in the tenant as a personal
// least-privilege baseline.
func TestRecordWiredGlobalNeverMarksADowngradeAsTheDefault(t *testing.T) {
	isolateConfigHome(t)

	recordWiredGlobal(onboardingBinding{
		RawKey:     "telara_mcp_fallback",
		ConfigID:   "11111111-2222-3333-4444-555555555555",
		ConfigName: "Ready",
		IsBase:     false,
	})

	state, err := agent.LoadWiredState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Global == nil {
		t.Fatal("a fallback binding should still be recorded")
	}
	if state.Global.IsDefault() {
		t.Fatal("a downgrade fallback must not be presented as the user's personal base")
	}
}

// The tenant master has no per-config identity. Recording a blank id would make
// `telara install` resolve the empty string as a config name on the next run.
func TestRecordWiredGlobalSkipsABindingWithNoConfigID(t *testing.T) {
	isolateConfigHome(t)

	recordWiredGlobal(onboardingBinding{RawKey: "telara_mcp_master", ConfigName: "Master"})

	state, err := agent.LoadWiredState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Global != nil {
		t.Fatalf("a binding with no config id must not be recorded, got %+v", state.Global)
	}
}

// An explicit selection has to survive as an explicit selection: the marker is
// what stops `telara config` from telling a user who chose a config that the
// CLI picked it for them.
func TestExplicitSelectionOverwritesTheAutoSelectedDefault(t *testing.T) {
	isolateConfigHome(t)

	recordWiredGlobal(onboardingBinding{
		ConfigID: "4e0acd6f-eb68-483e-a64c-e67490302aa1", ConfigName: "Personal", IsBase: true,
	})
	if err := agent.SaveWiredGlobal("9f8e7d6c-0000-1111-2222-333344445555", "Connected Engineering MCP"); err != nil {
		t.Fatal(err)
	}

	state, err := agent.LoadWiredState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Global.ConfigName != "Connected Engineering MCP" {
		t.Fatalf("explicit selection did not win: %q", state.Global.ConfigName)
	}
	if state.Global.IsDefault() {
		t.Fatal("a config the user named must not be reported as an auto-selected default")
	}
}

// State written before Source existed came from wireTools, which only ran on an
// explicit `telara config global`. It must not start reading as auto-selected.
func TestWiredStateWithoutASourceReadsAsExplicit(t *testing.T) {
	cfg := &agent.WiredConfig{ConfigID: "abc", ConfigName: "Master"}
	if cfg.IsDefault() {
		t.Fatal("pre-existing state with no source field must read as an explicit choice")
	}
	var nilCfg *agent.WiredConfig
	if nilCfg.IsDefault() {
		t.Fatal("a nil wired config must not report as a default")
	}
}

// The recorded id is what `telara install` reuses, so a second run installs the
// profile already in use instead of bootstrapping a fresh credential.
func TestInstallReusesTheRecordedDefault(t *testing.T) {
	state := &agent.WiredState{
		Global: &agent.WiredConfig{
			ConfigID:   "4e0acd6f-eb68-483e-a64c-e67490302aa1",
			ConfigName: "Personal (034c5aa3-d3d0-476b-8fe3-1ea1fa93ae06)",
			Source:     agent.SourceDefault,
		},
	}
	got, err := selectedInstallConfigForState("", state)
	if err != nil {
		t.Fatal(err)
	}
	if got != "4e0acd6f-eb68-483e-a64c-e67490302aa1" {
		t.Fatalf("install must reuse the recorded config id, got %q", got)
	}
}

// The base is identified by the id the server returns, never by its display
// name. The name embeds the USER's uuid, so neither the literal string
// "Personal" nor the uuid inside the name identifies the configuration.
func TestOnboardingCredentialCarriesTheBaseConfigIdentity(t *testing.T) {
	const (
		wantConfigID = "4e0acd6f-eb68-483e-a64c-e67490302aa1"
		displayName  = "Personal (034c5aa3-d3d0-476b-8fe3-1ea1fa93ae06)"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cli/configs/base/key" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"base_key":      "telara_mcp_base",
			"mcp_url":       "https://api.telara.dev/v1/mcp/sse",
			"mcp_config_id": wantConfigID,
			"config_name":   displayName,
		})
	}))
	defer server.Close()

	binding, err := onboardingCredential(context.Background(), api.NewClient(server.URL, "token"), "install")
	if err != nil {
		t.Fatal(err)
	}
	if !binding.IsBase {
		t.Fatal("the base-key route returns the user's own base; it must be marked as such")
	}
	if binding.ConfigID != wantConfigID {
		t.Fatalf("config id should come from the server, got %q", binding.ConfigID)
	}
	// Guard the actual trap: the uuid inside the display name is the user id.
	if binding.ConfigID == "034c5aa3-d3d0-476b-8fe3-1ea1fa93ae06" {
		t.Fatal("identity was taken from the display name's uuid, which is the USER id")
	}
}
