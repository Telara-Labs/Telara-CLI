package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/schedule"
)

var loginToken string
var loginForce bool

var loginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Sign in to Telara to access your organization's engineering knowledge",
	Example: "  telara login\n  telara login --token tlrc_abc123...\n  telara login --force",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Re-auth guard: check if already logged in (unless --force).
		if !loginForce {
			existingToken, err := auth.LoadToken(prefs.APIURL)
			if err == nil && existingToken != "" {
				client := api.NewClient(prefs.APIURL, existingToken)
				whoami, err := client.ValidateToken(context.Background())
				if err == nil {
					bootstrapAfterLogin(client, whoami.UserID, whoami.TenantID)
					fmt.Fprintf(os.Stdout, "Already authenticated as %s (%s). Run 'telara logout' first to switch accounts.\n", whoami.Email, whoami.OrgName)
					return nil
				}
				// Token is invalid/expired — delete stale token and proceed.
				_ = auth.DeleteToken(prefs.APIURL)
			}
		}

		if loginToken != "" {
			return runLoginWithToken(loginToken)
		}
		return runDeviceFlowLogin()
	},
}

func runLoginWithToken(token string) error {
	if err := auth.ValidateTokenFormat(token); err != nil {
		return err
	}

	client := api.NewClient(prefs.APIURL, token)
	whoami, err := client.ValidateToken(context.Background())
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return finishLogin(token, whoami, false)
}

func runDeviceFlowLogin() error {
	client := api.NewClient(prefs.APIURL, "")

	result, err := auth.StartDeviceFlow(context.Background(), client)
	if err != nil {
		return err
	}

	verifyURL := result.VerificationURI
	if verifyURL == "" {
		verifyURL = "https://www.telara.dev/device"
	}

	// A wide terminal gets the animated board, which shows the same URL and
	// code; anything else gets the plain lines and a spinner.
	var board *loginBoard
	var spinner *display.Spinner
	if display.Animate(os.Stdout) {
		board = newLoginBoard(os.Stdout, verifyURL, result.UserCode, result.ExpiresIn)
	} else {
		fmt.Fprintf(os.Stdout, "Open this URL in your browser:\n  %s\n\n", verifyURL)
		fmt.Fprintf(os.Stdout, "Enter code: %s\n\n", result.UserCode)
	}

	// Attempt to open the browser automatically — ignore failures.
	openBrowser(verifyURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if board == nil {
		spinner = display.NewSpinner()
		spinner.Start("Waiting for browser authorization")
	}

	token, err := auth.PollForToken(ctx, client, result.DeviceCode, result.Interval)
	if err != nil {
		board.finish(false)
		if spinner != nil {
			spinner.Fail("Authorization failed")
		}
		return fmt.Errorf("authorization failed: %w", err)
	}
	board.finish(true)
	if spinner != nil {
		spinner.Success("Authorized")
	}

	// Validate the token and fetch identity.
	authedClient := api.NewClient(prefs.APIURL, token)
	whoami, err := authedClient.ValidateToken(context.Background())
	if err != nil {
		return fmt.Errorf("failed to validate token: %w", err)
	}

	return finishLogin(token, whoami, board != nil)
}

// finishLogin saves the token, prints the welcome banner, and auto-wires detected
// tools only if no saved snapshot needs review. boardShown says the
// animated board already drew the logo.
func finishLogin(token string, whoami *api.WhoamiResponse, boardShown bool) error {
	if err := auth.SaveToken(prefs.APIURL, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}
	// Persist the API URL so future sessions without TELARA_API_URL find the right token.
	if err := config.Save(prefs); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	printLoginBanner(whoami.Email, whoami.OrgName, boardShown)

	client := api.NewClient(prefs.APIURL, token)
	bootstrapAfterLogin(client, whoami.UserID, whoami.TenantID)

	ensureScanSchedule(token)
	return nil
}

func bootstrapAfterLogin(client *api.Client, userID, tenantID string) {
	restoreSnapshotAfterLogin(userID, tenantID)
	// An unresolved snapshot may represent a different selected configuration.
	// Preserve it and do not silently replace that choice with a new default.
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(tenantID) == "" {
		fmt.Fprintln(os.Stderr, "Warning: account or workspace identity is missing; client setup was skipped.")
		return
	}
	remainingSnapshot, snapshotErr := agent.LoadWorkspaceSnapshot(userID, tenantID)
	if snapshotErr != nil || remainingSnapshot != nil {
		fmt.Fprintln(os.Stderr, "Warning: saved client settings need review; run `telara install` to reconnect the intended configuration.")
	} else {
		ensureMCPConfig(client, tenantID)
	}
}

// ensureScanSchedule installs the recurring, unattended discovery scan.
//
// Users are not expected to run `telara scan` by hand. Estate coverage is only
// truthful if scans recur — evidence freshness and per-scope completeness decay
// to stale otherwise, and the product would start claiming coverage it does not
// have.
//
// The scan destination is compiled in (see config.ScanSubmitEndpoint), so
// scheduling it cannot be abused to redirect a fleet's evidence.
//
// A failure here never fails login: the user is authenticated either way, and
// an unattended scan is worthless if it locks someone out of the CLI.
func ensureScanSchedule(token string) {
	// The scan submits to the pinned endpoint, so it needs a token stored under
	// THAT host. When the login endpoint differs (dev builds), mirror it across
	// so the scheduled run can authenticate.
	scanEndpoint := config.ScanSubmitEndpoint()
	if scanEndpoint != prefs.APIURL {
		if _, err := auth.LoadToken(scanEndpoint); err != nil {
			_ = auth.SaveToken(scanEndpoint, token)
		}
	}

	status, err := schedule.Install()
	if err != nil {
		// Non-fatal and quiet about the reason on unsupported platforms.
		fmt.Fprintf(os.Stderr, "Note: automatic daily estate scan not installed (%v)\n", err)
		fmt.Fprintln(os.Stderr, "      Run `telara scan` manually, or deploy the schedule via MDM.")
		return
	}
	fmt.Fprintf(os.Stdout, "Automatic daily estate scan enabled (%s)\n", status.Path)
	fmt.Fprintln(os.Stdout, "  Inspect what it sends:  telara scan --dry-run")
	fmt.Fprintln(os.Stdout, "  Turn it off:            telara scan --uninstall-schedule")
}

// printLoginBanner prints the Telara logo, auth identity, and quick-start commands.
// Colors are only emitted when stdout is a real terminal (handled by fatih/color).
// After the animated board, which already drew the logo, it starts at the
// identity line.
func printLoginBanner(email, orgName string, boardShown bool) {
	// Logo: \telara. — backslash is part of the mark, period in brand purple.
	logo := display.ColorBold.Sprint("\\telara") + display.ColorBrand.Sprint(".")
	divider := display.ColorDim.Sprint("────────────────────────────────────────")

	fmt.Fprintln(os.Stdout)
	if !boardShown {
		fmt.Fprintln(os.Stdout, "  "+logo)
		fmt.Fprintln(os.Stdout, "  "+divider)
	}
	fmt.Fprintf(os.Stdout, "  Authenticated as %s", display.ColorBold.Sprint(email))
	if orgName != "" {
		fmt.Fprintf(os.Stdout, " %s", display.ColorDim.Sprint("· "+orgName))
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout)

	type quickCmd struct {
		cmd  string
		desc string
	}
	cmds := []quickCmd{
		{"telara config", "See what's configured at each layer"},
		{"telara config global <name>", "Change your global configuration"},
		{"telara config project <name>", "Set a project-specific config"},
		{"telara doctor", "Diagnose connection issues"},
	}

	fmt.Fprintln(os.Stdout, "  "+display.ColorDim.Sprint("Quick start:"))
	for _, c := range cmds {
		fmt.Fprintf(os.Stdout, "    %-26s %s\n", display.ColorCmd.Sprint(c.cmd), display.ColorDim.Sprint(c.desc))
	}
	fmt.Fprintln(os.Stdout)
}

// autoWireTools detects installed AI tools and writes the server-selected
// personal configuration to each one during a fresh bootstrap. It does not
// fall back to a tenant master key when personal activation fails.
//
// If the tools are already wired with a key that belongs to the current tenant
// and force is false, the call is a no-op — we never mint a new key just
// because the user ran 'telara login' again.
//
// Failures are non-fatal — the user can always run 'telara config global' manually.
func autoWireTools(client *api.Client, tenantID string, force bool) {
	detected := agent.DetectedWriters()
	if len(detected) == 0 {
		return
	}

	// If not forced, skip re-generation when all detected tools are already
	// wired with a key that belongs to the current tenant. This prevents
	// minting a new orphaned key on every 'telara login' while ensuring an
	// account switch always produces a fresh key.
	if !force {
		allWired := true
		for _, w := range detected {
			entries, err := w.Read(agent.ScopeGlobal)
			if err != nil || entries["telara"].URL == "" {
				allWired = false
				break
			}
			if !mcpEntryMatchesWriterEndpoint(w, entries["telara"]) {
				allWired = false
				break
			}
			if !keyBelongsToTenant(entries["telara"], tenantID) {
				allWired = false
				break
			}
		}
		if allWired {
			return
		}
	}

	binding, err := onboardingCredential(context.Background(), client, toolKeyName(detected[0].Name()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not get a Telara credential to connect your tools: %v\n", err)
		return
	}

	mcpURL := binding.MCPURL
	if mcpURL == "" {
		mcpURL = defaultMCPURL()
	}

	toolNames := agent.ResolveToolNames(context.Background(), mcpURL, binding.RawKey)

	var wired []string
	for _, w := range detected {
		entry := newMCPEntryForWriter(mcpURL, binding.RawKey, w)
		if err := w.Write(agent.ScopeGlobal, "telara", entry); err != nil {
			// Say so. A writer now refuses rather than overwriting a config it
			// cannot parse, so a skip here means a tool the user believes is
			// connected is not — and silence would be the only clue.
			fmt.Fprintf(os.Stderr, "Warning: could not configure %s: %v\n", w.Name(), err)
			continue
		}
		if pw, ok := w.(agent.PermissionWriter); ok {
			_ = pw.WritePermissions(agent.ScopeGlobal, "telara", toolNames)
		}
		wired = append(wired, w.Name())
	}

	if len(wired) > 0 {
		// Record what login just wired (TENG-3017). Without this the global
		// layer read back as an anonymous "connected" with no config behind
		// it: `telara config` could not name it, `telara install` treated the
		// user as having no selection and re-bootstrapped, and the picker had
		// nothing to default to. Only a real base binding is stored as the
		// user's default; a downgrade fallback is recorded as an explicit
		// binding so it is never presented as their personal baseline.
		recordWiredGlobal(binding)

		fmt.Fprintf(os.Stdout, "  Connected to %s: ", binding.ConfigName)
		for i, name := range wired {
			if i > 0 {
				fmt.Fprint(os.Stdout, ", ")
			}
			fmt.Fprint(os.Stdout, name)
		}
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout)
	}
}

// recordWiredGlobal persists the configuration a credential-based auto-wire
// bound the global layer to. A binding with no config id (the tenant-master
// downgrade, which has no per-config identity) is not recorded at all: a stored
// id is the thing every later lookup keys on, and a blank one would make
// `telara install` resolve the empty string as a config name.
func recordWiredGlobal(binding onboardingBinding) {
	if binding.ConfigID == "" {
		return
	}
	if binding.IsDefaultBinding {
		if err := agent.SaveWiredGlobalDefault(binding.ConfigID, binding.ConfigName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: client settings changed but the selected configuration could not be saved: %v\n", err)
		}
		return
	}
	if err := agent.SaveWiredGlobal(binding.ConfigID, binding.ConfigName); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: client settings changed but the selected configuration could not be saved: %v\n", err)
	}
}

// ensureMCPConfig bootstraps clients only when no existing Telara configuration
// is present. Existing credentials require authoritative validation before repair.
func ensureMCPConfig(client *api.Client, tenantID string) {
	// Without an authoritative subject/config/revocation check, an existing
	// key is unknown, even if its text contains this tenant. Never replace it
	// or a selected configuration automatically while that proof is missing.
	state, err := agent.LoadWiredState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read the selected configuration; client settings were left unchanged: %v\n", err)
		return
	}
	writers := agent.DetectedWriters()
	if len(writers) == 0 {
		return
	}
	if state != nil && state.Global != nil && strings.TrimSpace(state.Global.ConfigID) != "" {
		fmt.Fprintln(os.Stderr, "Warning: existing client credentials were not verified; run `telara install` to reconnect the selected configuration if needed.")
		return
	}
	for _, w := range writers {
		entries, err := w.Read(agent.ScopeGlobal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read %s settings; client settings were left unchanged: %v\n", w.Name(), err)
			return
		}
		if _, ok := entries["telara"]; ok {
			fmt.Fprintln(os.Stderr, "Warning: existing client credentials were not verified; client settings were left unchanged.")
			return
		}
	}
	// No state and no existing Telara entry: this is a fresh bootstrap.
	autoWireTools(client, tenantID, true)
}

// keyBelongsToTenant checks whether the API key stored in an MCPEntry belongs
// to the given tenant. The key format is "telara_mcp_{tenant_id}_{hash}".
// Returns false if the key is missing, malformed, or belongs to a different tenant.
func keyBelongsToTenant(entry agent.MCPEntry, tenantID string) bool {
	bearer := entry.Headers["Authorization"]
	if bearer == "" {
		return false
	}
	key := strings.TrimPrefix(bearer, "Bearer ")
	// Expected format: telara_mcp_{32-char tenant_id}_{32-char hash}
	parts := strings.SplitN(key, "_", 4) // ["telara", "mcp", "{tenant_id}", "{hash}"]
	if len(parts) < 4 || parts[0] != "telara" || parts[1] != "mcp" {
		return false
	}
	return parts[2] == tenantID
}

// restoreSnapshotAfterLogin preserves saved settings until their credentials
// and configuration ownership can be verified against the server.
func restoreSnapshotAfterLogin(userID, tenantID string) bool {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(tenantID) == "" {
		return false
	}
	snap, err := agent.LoadWorkspaceSnapshot(userID, tenantID)
	if err != nil || snap == nil {
		return false
	}

	if snap.UserID != userID || snap.TenantID == "" || snap.TenantID != tenantID {
		fmt.Fprintln(os.Stderr, "Warning: saved client settings do not match this account and workspace; they were not restored.")
		return false
	}
	// Logout can revoke the cached key before saving it in this snapshot. The
	// snapshot alone cannot establish that any key remains valid or belongs to
	// the signed-in user and intended configuration. Never write it back as if
	// it were a working connection, and retain it for explicit recovery.
	return false
}

// openBrowser tries to open url in the user's default browser.
// Failures are silently ignored — the user can always open it manually.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func init() {
	loginCmd.Flags().StringVarP(&loginToken, "token", "t", "", "CLI token (tlrc_...)")
	loginCmd.Flags().BoolVar(&loginForce, "force", false, "Skip re-authentication check and proceed with login")
	rootCmd.AddCommand(loginCmd)
}
