package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
)

var loginToken string

var loginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Authenticate with the Telara API",
	Example: "  telara login\n  telara login --token tlrc_abc123...",
	RunE: func(cmd *cobra.Command, args []string) error {
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

	if err := auth.SaveToken(prefs.APIURL, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Authenticated as %s (%s)\n", whoami.Email, whoami.OrgName)
	return nil
}

func runDeviceFlowLogin() error {
	client := api.NewClient(prefs.APIURL, "")

	result, err := auth.StartDeviceFlow(context.Background(), client)
	if err != nil {
		return err
	}

	verifyURL := result.VerificationURI
	if verifyURL == "" {
		verifyURL = "https://app.telara.dev/device"
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

	if err := auth.SaveToken(prefs.APIURL, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Authenticated as %s (%s)\n", whoami.Email, whoami.OrgName)
	return nil
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
	rootCmd.AddCommand(loginCmd)
}
