package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/agent"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
	"golang.org/x/term"
)

var loginToken string
var loginForce bool

var loginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Authenticate with the Telara API",
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

	fmt.Fprintln(os.Stdout, "Waiting for authorization...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	token, err := auth.PollForToken(ctx, client, result.DeviceCode, result.Interval)
	if err != nil {
		return fmt.Errorf("authorization failed: %w", err)
	}

	// Validate the token and fetch identity.
	authedClient := api.NewClient(prefs.APIURL, token)
	whoami, err := authedClient.ValidateToken(context.Background())
	if err != nil {
		return fmt.Errorf("failed to validate token: %w", err)
	}

	return finishLogin(token, whoami)
}

// finishLogin saves the token, prints the welcome banner, and auto-restores any snapshot.
func finishLogin(token string, whoami *api.WhoamiResponse) error {
	if err := auth.SaveToken(prefs.APIURL, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}
	printLoginBanner(whoami.Email, whoami.OrgName)
	restoreSnapshotAfterLogin(whoami.UserID, whoami.TenantID)
	return nil
}

// printLoginBanner prints the Telera logo, auth identity, and quick-start commands.
// Colors are only emitted when stdout is a real terminal.
func printLoginBanner(email, orgName string) {
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))

	bold := func(s string) string {
		if isTTY {
			return "\033[1m" + s + "\033[0m"
		}
		return s
	}
	dim := func(s string) string {
		if isTTY {
			return "\033[2m" + s + "\033[0m"
		}
		return s
	}
	cyan := func(s string) string {
		if isTTY {
			return "\033[36m" + s + "\033[0m"
		}
		return s
	}
	purple := func(s string) string {
		if isTTY {
			return "\033[35m" + s + "\033[0m"
		}
		return s
	}

	// Logo: \telara. — backslash is part of the mark, period in brand purple.
	logo := bold("\\telara") + purple(".")

	divider := dim("────────────────────────────────────────")

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "  "+logo)
	fmt.Fprintln(os.Stdout, "  "+divider)
	fmt.Fprintf(os.Stdout, "  Authenticated as %s", bold(email))
	if orgName != "" {
		fmt.Fprintf(os.Stdout, " %s", dim("· "+orgName))
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout)

	type quickCmd struct {
		cmd  string
		desc string
	}
	cmds := []quickCmd{
		{"telara setup", "Configure your AI tools"},
		{"telara config list", "List your configurations"},
		{"telara context list", "View saved contexts"},
		{"telara doctor", "Diagnose connection issues"},
	}

	fmt.Fprintln(os.Stdout, "  "+dim("Quick start:"))
	for _, c := range cmds {
		fmt.Fprintf(os.Stdout, "    %-26s%s\n", cyan(c.cmd), dim(c.desc))
	}
	fmt.Fprintln(os.Stdout)
}

// restoreSnapshotAfterLogin silently restores the user's MCP config snapshot if one exists.
// Managed entries are skipped when the tenant differs from the snapshot's tenant.
func restoreSnapshotAfterLogin(userID, tenantID string) {
	snap, err := agent.LoadSnapshot(userID)
	if err != nil || snap == nil {
		return
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
	}
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
