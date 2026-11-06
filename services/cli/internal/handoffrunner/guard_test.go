package handoffrunner

// The guard end to end against fakecodex, a test double for the external
// provider only: the run's Codex is started confined (on macOS), its tool
// surface is proven before any input is fetched, and a Codex that offers a native
// tool never receives input and never reaches the model upstream.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// runControl stands in for the person's Telara session in this package's tests;
// the real exchange is proven in agent-service (TestPGLocalRunnerEndToEndWithFakeCodex).
type runControl struct {
	Control
	inputs  int
	reports []string
}

func (c *runControl) ClaimHandoffRun(context.Context, string) (*api.HandoffRunClaim, error) {
	return &api.HandoffRunClaim{Credential: "telara_handoff_t.i.s", IncarnationID: "inc-1", ExpiresAt: time.Now().Add(time.Minute), ChildTaskID: "task-1"}, nil
}

func (c *runControl) GetHandoffRunInput(context.Context, string, string, api.HandoffRunAttachment) (*api.HandoffRunInput, error) {
	c.inputs++
	return &api.HandoffRunInput{BriefJSON: `{"objective":"fake:complete"}`}, nil
}

func (c *runControl) ReportHandoffRun(_ context.Context, _, _, status string) (bool, string, error) {
	c.reports = append(c.reports, status)
	return false, status, nil
}

func guardedRunner(t *testing.T, control Control, models *recordingUpstream) (*Runner, *upstream) {
	t.Helper()
	up := newUpstream(http.StatusOK)
	t.Cleanup(up.Close)
	r := &Runner{Control: control, CodexBin: fakeCodexBin, Dir: t.TempDir(), MCPURL: up.URL + "/v1/mcp", Key: testKey(t), ModelTransport: models, Heartbeat: 50 * time.Millisecond}
	if err := r.PrepareHome(); err != nil {
		t.Fatal(err)
	}
	if err := writeSignIn(r.CodexHome()); err != nil {
		t.Fatal(err)
	}
	return r, up
}

func TestRunWithNativeToolsOffIsProvenThenServed(t *testing.T) {
	control := &runControl{}
	models := &recordingUpstream{}
	r, _ := guardedRunner(t, control, models)
	if err := r.Serve(context.Background(), api.HandoffRun{ExecutionID: "exec-1", CatalogKey: CodexCatalogKey}); err != nil {
		t.Fatal(err)
	}
	if control.inputs != 1 || !slices.Equal(control.reports, []string{api.HandoffRunCompleted}) {
		t.Fatalf("inputs %d reports %v", control.inputs, control.reports)
	}
	var probe, run map[string]any
	var start map[string]any
	for _, rec := range records(t, r.CodexHome()) {
		switch rec["kind"] {
		case "start":
			start = rec
		case "model_request":
			if probe == nil {
				probe = rec
			} else {
				run = rec
			}
		}
	}
	// The probe went first and never left the machine; the run's own request
	// was checked and forwarded to the one upstream.
	if probe == nil || run == nil || probe["thread"] == run["thread"] || probe["catalog"] != true {
		t.Fatalf("model requests probe=%v run=%v", probe, run)
	}
	if models.count() != 1 || models.seen[0].URL.Host != "chatgpt.com" {
		t.Fatalf("upstream saw %d", models.count())
	}
	if runtime.GOOS == "darwin" {
		// The fake ran under sandbox-exec: its environment is the runner's only.
		env, _ := start["env"].([]any)
		for _, e := range env {
			if s := e.(string); strings.HasPrefix(s, "TMPDIR=") && !strings.Contains(s, "codex-home") {
				t.Fatalf("temporary directory outside the confinement: %s", s)
			}
		}
	}
}

func TestRunOfferingANativeToolGetsNoInputAndNoModel(t *testing.T) {
	saved := disabledFeatures
	disabledFeatures = slices.DeleteFunc(slices.Clone(saved), func(f string) bool { return f == "shell_tool" })
	t.Cleanup(func() { disabledFeatures = saved })
	control := &runControl{}
	models := &recordingUpstream{}
	r, _ := guardedRunner(t, control, models)
	err := r.Serve(context.Background(), api.HandoffRun{ExecutionID: "exec-2", CatalogKey: CodexCatalogKey})
	if err == nil || !strings.Contains(err.Error(), `"exec_command"`) {
		t.Fatalf("want the guard to name exec_command, got %v", err)
	}
	if control.inputs != 0 || !slices.Equal(control.reports, []string{api.HandoffRunCancelled}) {
		t.Fatalf("inputs %d reports %v", control.inputs, control.reports)
	}
	if models.count() != 0 {
		t.Fatal("the model upstream was reached")
	}
	for _, rec := range records(t, r.CodexHome()) {
		if rec["kind"] == "turn_start" && rec["mode"] != "hang" {
			t.Fatalf("the run's turn started: %v", rec)
		}
	}
}

func TestCheckPassesWithTheFakeAndNamesAnExtraTool(t *testing.T) {
	r, _ := guardedRunner(t, &runControl{}, &recordingUpstream{})
	var out strings.Builder
	err := r.Check(&out)
	// The fake answers no fs/ or command/ request, so every native attempt is
	// refused; the check is about the tool surface here.
	if err != nil && !strings.Contains(err.Error(), "inconclusive") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	saved := disabledFeatures
	disabledFeatures = slices.DeleteFunc(slices.Clone(saved), func(f string) bool { return f == "shell_tool" })
	t.Cleanup(func() { disabledFeatures = saved })
	out.Reset()
	err = r.Check(&out)
	if err == nil || !strings.Contains(out.String(), "exec_command") || !strings.Contains(out.String(), "Tool surface: FAILED") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func writeSignIn(home string) error {
	return os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"test":"double"}`), 0o600)
}
