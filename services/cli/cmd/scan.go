package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/schedule"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/version"
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan local AI client MCP configs and submit an estate discovery report",
	Long: `Read-only scan of AI client MCP configurations on this machine.

Reports which MCP servers are configured (credential class only — never values),
coverage per client/scope, and submits the result to Telara for AI estate inventory.

Use --dry-run to print exactly what would leave this machine without submitting.`,
	RunE: runScan,
}

func init() {
	scanCmd.Flags().Bool("dry-run", false, "Print the discovery report as indented JSON and submit nothing")
	scanCmd.Flags().Bool("json", false, "Emit machine-readable JSON output")
	scanCmd.Flags().Bool("install-schedule", false, "Install the recurring daily scan and exit")
	scanCmd.Flags().Bool("uninstall-schedule", false, "Remove the recurring daily scan and exit")
	scanCmd.Flags().Bool("schedule-status", false, "Show whether the recurring scan is installed and exit")
	rootCmd.AddCommand(scanCmd)
}

func runScan(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	asJSON, _ := cmd.Flags().GetBool("json")

	// Schedule management short-circuits the scan itself. These exist so the
	// recurring job is inspectable and revocable by the person whose machine it
	// runs on — an endpoint agent nobody can see or turn off is malware
	// behaviour, not security tooling.
	if v, _ := cmd.Flags().GetBool("schedule-status"); v {
		st := schedule.Current()
		state := "not installed"
		if st.Installed {
			state = "installed"
		}
		display.PrintKV(os.Stdout, "Recurring scan:", state)
		if st.Path != "" {
			display.PrintKV(os.Stdout, "Unit file:", st.Path)
		}
		display.PrintKV(os.Stdout, "Submits to:", config.ScanSubmitEndpoint())
		return nil
	}
	if v, _ := cmd.Flags().GetBool("uninstall-schedule"); v {
		if err := schedule.Uninstall(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Recurring estate scan removed.")
		return nil
	}
	if v, _ := cmd.Flags().GetBool("install-schedule"); v {
		st, err := schedule.Install()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Recurring estate scan installed (%s)\n", st.Path)
		return nil
	}

	// The scan destination is compiled in and cannot be redirected at runtime.
	// prefs.APIURL / --api-url / TELARA_API_URL deliberately do NOT apply here:
	// discovery evidence must reach the tenant's own estate, and a runtime knob
	// would let anyone redirect a fleet's output or point their own machine at
	// nothing to vanish from inventory.
	endpoint := config.ScanSubmitEndpoint()
	if config.ScanEndpointIsOverridden() {
		fmt.Fprintf(os.Stderr, "WARNING: this is a non-production build; scans submit to %s\n", endpoint)
	}

	token, err := auth.LoadToken(endpoint)
	if err != nil {
		return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
	}

	startedAt := time.Now().UTC()
	results := discovery.ScanAll()
	completedAt := time.Now().UTC()

	report := discovery.BuildReport(
		discovery.CollectorIdentity{
			CollectorKind:     "telara-cli-endpoint",
			CollectorVersion:  version.Version,
			InstallationKey:   installationKey(),
			DeviceLabelClass:  deviceLabelClass(),
			NonHumanPrincipal: false,
		},
		newScanID(),
		startedAt,
		completedAt,
		results,
	)

	// Configurations pointing at this tenant's own Telara endpoint are
	// discovered-managed; everything else stays discovered-unmediated. Matching
	// is on host, not on the server's display name.
	discovery.ApplyManagedEndpoints(&report, []string{endpoint})

	summary := discovery.Summarize(report)

	if dryRun {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("encode dry-run report: %w", err)
		}
		// Coverage on stderr so stdout remains valid JSON for piping/inspection.
		printCoverage(os.Stderr, summary, len(report.ResourceAssertions))
		fmt.Fprintln(os.Stderr, "Dry run — nothing was submitted.")
		return nil
	}

	client := api.NewClient(endpoint, token)
	spinner := display.NewSpinner()
	if !asJSON {
		spinner.Start("Submitting discovery report...")
	}
	resp, err := client.SubmitDiscoveryReport(context.Background(), report)
	if err != nil {
		if !asJSON {
			spinner.Fail("Failed to submit discovery report")
		}
		return err
	}
	if !asJSON {
		spinner.Success("Discovery report submitted")
	}

	if asJSON {
		out := map[string]interface{}{
			"coverage": summary,
			"servers":  len(report.ResourceAssertions),
			"scanId":   report.ScanID,
			"response": resp,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	printCoverage(os.Stdout, summary, len(report.ResourceAssertions))
	display.PrintKV(os.Stdout, "Collection run:", resp.CollectionRunID)
	if resp.Duplicate {
		display.PrintKV(os.Stdout, "Duplicate:", "true (already ingested)")
	}
	display.PrintKV(os.Stdout, "Accepted assertions:", fmt.Sprintf("%d", resp.AcceptedAssertionCount))
	if resp.RejectedAssertionCount > 0 {
		display.PrintKV(os.Stdout, "Rejected assertions:", fmt.Sprintf("%d", resp.RejectedAssertionCount))
	}
	return nil
}

func printCoverage(w io.Writer, summary discovery.CoverageSummary, serverCount int) {
	display.PrintKV(w, "Scopes scanned:", fmt.Sprintf("%d", summary.ScopesTotal))
	display.PrintKV(w, "Complete:", fmt.Sprintf("%d", summary.ScopesComplete))
	display.PrintKV(w, "Partial:", fmt.Sprintf("%d", summary.ScopesPartial))
	display.PrintKV(w, "Failed:", fmt.Sprintf("%d", summary.ScopesFailed))
	display.PrintKV(w, "Servers found:", fmt.Sprintf("%d", serverCount))
}

func newScanID() string {
	// google/uuid is not a dependency; use a timestamp + random suffix.
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("scan-%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(b[:]))
}

func deviceLabelClass() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return runtime.GOOS
	}
}

// installationKey returns a stable per-machine opaque key. It is never a raw
// hostname or username — preferably a hash of OS machine-id, otherwise a
// persisted random value under the CLI config directory.
func installationKey() string {
	h := sha256.New()
	h.Write([]byte("telara-cli-endpoint/v1"))
	h.Write([]byte{0})
	h.Write([]byte(runtime.GOOS))
	h.Write([]byte{0})
	h.Write([]byte(runtime.GOARCH))

	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if raw, err := os.ReadFile(path); err == nil {
			trimmed := strings.TrimSpace(string(raw))
			if trimmed != "" {
				h.Write([]byte{0})
				h.Write([]byte(trimmed))
				return hex.EncodeToString(h.Sum(nil))
			}
		}
	}

	if dir, err := config.ConfigDir(); err == nil {
		path := filepath.Join(dir, "installation.key")
		if raw, err := os.ReadFile(path); err == nil {
			key := strings.TrimSpace(string(raw))
			if len(key) >= 32 {
				return key
			}
		}
		var buf [32]byte
		if _, err := rand.Read(buf[:]); err == nil {
			key := hex.EncodeToString(buf[:])
			_ = os.WriteFile(path, []byte(key+"\n"), 0o600)
			return key
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}
