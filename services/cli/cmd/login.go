package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
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

	return finishLogin(token, whoami)
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

	fmt.Fprintf(os.Stdout, "Open this URL in your browser:\n  %s\n\n", verifyURL)
	fmt.Fprintf(os.Stdout, "Enter code: %s\n\n", result.UserCode)

	// Attempt to open the browser automatically — ignore failures.
	openBrowser(verifyURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	spinner := display.NewSpinner()
	spinner.Start("Waiting for browser authorization")

	token, err := auth.PollForToken(ctx, client, result.DeviceCode, result.Interval)
	if err != nil {
		spinner.Fail("Authorization failed")
		return fmt.Errorf("authorization failed: %w", err)
	}
	spinner.Success("Authorized")

	// Validate the token and fetch identity.
	authedClient := api.NewClient(prefs.APIURL, token)
	whoami, err := authedClient.ValidateToken(context.Background())
	if err != nil {
		return fmt.Errorf("failed to validate token: %w", err)
	}

	return finishLogin(token, whoami)
}

// finishLogin saves the token, prints the welcome banner, auto-restores any snapshot,
// and auto-wires detected tools if no snapshot existed.
func finishLogin(token string, whoami *api.WhoamiResponse) error {
	if err := auth.SaveToken(prefs.APIURL, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}
	// Persist the API URL so future sessions without TELARA_API_URL find the right token.
	if err := config.Save(prefs); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	printLoginBanner(whoami.Email, whoami.OrgName)

	restored := restoreSnapshotAfterLogin(whoami.UserID, whoami.TenantID)
	if !restored {
		// First-time login or no snapshot: auto-wire detected tools with the
		// tenant-scoped default config so Layer 1 is set up without any extra steps.
		client := api.NewClient(prefs.APIURL, token)
		autoWireTools(client)
	}
	return nil
}

// printLoginBanner prints the Telara logo, auth identity, and quick-start commands.
// Colors are only emitted when stdout is a real terminal (handled by fatih/color).
func printLoginBanner(email, orgName string) {
	// Logo: \telara. — backslash is part of the mark, period in brand purple.
	logo := display.ColorBold.Sprint("\\telara") + display.ColorBrand.Sprint(".")
	divider := display.ColorDim.Sprint("────────────────────────────────────────")

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "  "+logo)
	fmt.Fprintln(os.Stdout, "  "+divider)
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
		{"telara config list", "View your knowledge configurations"},
		{"telara setup", "Change config or add another tool"},
		{"telara init", "Connect a specific repo to a different config"},
		{"telara doctor", "Diagnose connection issues"},
	}

	fmt.Fprintln(os.Stdout, "  "+display.ColorDim.Sprint("Quick start:"))
	for _, c := range cmds {
		fmt.Fprintf(os.Stdout, "    %-26s %s\n", display.ColorCmd.Sprint(c.cmd), display.ColorDim.Sprint(c.desc))
	}
	fmt.Fprintln(os.Stdout)
}

// autoWireTools detects installed AI tools and writes the tenant-scoped default
// MCP config to each one. Called on first login when no snapshot exists.
// Failures are non-fatal — the user can always run 'telara setup' manually.
func autoWireTools(client *api.Client) {
	detected := agent.DetectedWriters()
	if len(detected) == 0 {
		return
	}

	// Use the resolve endpoint to find the right config without requiring the user
	// to pick one. We prefer: managed bucket first, then available bucket.
	resolved, err := client.ResolveConfigs(context.Background())
	if err != nil {
		return
	}

	// Pick the first usable config (managed takes priority over available).
	candidates := append(resolved.Managed, resolved.Available...)
	if len(candidates) == 0 {
		return
	}
	cfg := candidates[0]

	// Find the tenant-scoped deployment for this config.
	deps, err := client.ListDeployments(context.Background(), cfg.ID)
	if err != nil || len(deps.Deployments) == 0 {
		return
	}
	var dep *api.Deployment
	for i := range deps.Deployments {
		if deps.Deployments[i].ScopeType == "tenant" {
			dep = &deps.Deployments[i]
			break
		}
	}
	// Fall back to first deployment if no tenant-scoped one exists.
	if dep == nil {
		dep = &deps.Deployments[0]
	}

	var wired []string
	for _, w := range detected {
		keyResp, err := client.GenerateKey(context.Background(), cfg.ID, api.GenerateKeyRequest{
			Name:      toolKeyName(w.Name()),
			ScopeType: dep.ScopeType,
			ScopeID:   dep.ScopeID,
		})
		if err != nil {
			continue
		}
		mcpURL := keyResp.MCPURL
		if mcpURL == "" {
			mcpURL = prefs.APIURL + "/v1/mcp/sse"
		}
		entry := agent.MCPEntry{
			Type:    "sse",
			URL:     mcpURL,
			Headers: map[string]string{"Authorization": "Bearer " + keyResp.RawKey},
		}
		if err := w.Write(agent.ScopeGlobal, "telara", entry); err != nil {
			continue
		}
		if pw, ok := w.(agent.PermissionWriter); ok {
			_ = pw.WritePermissions(agent.ScopeGlobal, "telara")
		}
		wired = append(wired, w.Name())
	}

	if len(wired) > 0 {
		fmt.Fprintf(os.Stdout, "  Connected to %s: ", cfg.Name)
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

// restoreSnapshotAfterLogin silently restores the user's MCP config snapshot if one exists.
// Managed entries are skipped when the tenant differs from the snapshot's tenant.
// Returns true if any entries were restored.
func restoreSnapshotAfterLogin(userID, tenantID string) bool {
	snap, err := agent.LoadSnapshot(userID)
	if err != nil || snap == nil {
		return false
	}

	sametenant := snap.TenantID == "" || snap.TenantID == tenantID

	origDir, _ := os.Getwd()

	type restored struct{ tool, scope, path string }
	var entries []restored

	for _, se := range snap.Entries {
		// Skip managed layer if tenant doesn't match.
		if se.Scope == "managed" && !sametenant {
			continue
		}

		w := agent.WriterByName(se.Tool)
		if w == nil {
			continue
		}

		var scope agent.Scope
		switch se.Scope {
		case "global":
			scope = agent.ScopeGlobal
		case "project":
			scope = agent.ScopeProject
			if se.ProjectDir != "" {
				if err := os.Chdir(se.ProjectDir); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: cannot restore project config for %s in %s: %v\n", se.Tool, se.ProjectDir, err)
					continue
				}
			}
		case "managed":
			scope = agent.ScopeManaged
		default:
			continue
		}

		if err := w.Write(scope, se.ServerName, se.Entry); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to restore %s %s config: %v\n", se.Scope, se.Tool, err)
			continue
		}

		// Show path for user-visible layers only.
		if se.Scope != "managed" {
			if path, err := w.ConfigPath(scope); err == nil {
				entries = append(entries, restored{se.Tool, se.Scope, path})
			}
		}

		if scope == agent.ScopeProject && se.ProjectDir != "" {
			_ = agent.RegisterProject(se.ProjectDir, se.Tool)
		}
	}

	if origDir != "" {
		_ = os.Chdir(origDir)
	}

	_ = agent.DeleteSnapshot(userID)

	if len(entries) > 0 {
		fmt.Fprintf(os.Stdout, "  Restored %d MCP config(s):\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(os.Stdout, "    %s (%s)  ->  %s\n", e.tool, e.scope, e.path)
		}
		fmt.Fprintln(os.Stdout)
		return true
	}
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
