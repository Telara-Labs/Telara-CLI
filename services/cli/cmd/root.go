package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/display"
)

var (
	rootAPIURL  string
	rootContext string
	verbose     bool
	caCertPath  string
	prefs       *config.Prefs
)

// IsVerbose returns true when the --verbose flag is set.
func IsVerbose() bool { return verbose }

var rootCmd = &cobra.Command{
	Use:   "telara",
	Short: "Telara CLI — scoped knowledge and tooling for your AI coding assistant",
	Long: `Telara is a secure MCP server that gives agentic coding tools (Claude Code, Cursor,
Windsurf, VS Code) two things: searchable knowledge from your engineering stack
(repos, Jira, Confluence, Slack, etc.) and live tooling against your integrations
— all governed by configurable access controls and policies.

Get started:
  telara login                Sign in to your Telara workspace
  telara setup claude-code    Connect Claude Code to your stack
  telara doctor               Verify the connection is working`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Fire-and-forget background version check.
		// This goroutine intentionally races against process exit.
		go checkVersionInBackground()
	},
}

// Execute is the entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		printError(err)
		os.Exit(1)
	}
}

// printError writes a formatted error to stderr, with extra detail when --verbose
// is set or when the error is a network-level failure.
func printError(err error) {
	// Network-level failures.
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp") {
		display.PrintError("Could not reach the Telara API. Check your connection and --api-url.")
		display.ShowHints("", []display.ActionHint{
			{Label: "Diagnose", Command: []string{"telara", "doctor"}, Description: "telara doctor"},
		})
		return
	}

	// API errors — map status codes to friendly messages.
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		if verbose {
			display.PrintError(fmt.Sprintf("HTTP %d: %s", apiErr.StatusCode, apiErr.Body))
			return
		}
		friendly := friendlyAPIError(apiErr)
		friendly = strings.TrimPrefix(friendly, "Error: ")
		display.PrintError(friendly)

		switch {
		case apiErr.StatusCode == 401:
			display.ShowHints("", []display.ActionHint{
				{Label: "Sign in again", Command: []string{"telara", "login"}, Description: "telara login"},
			})
		case apiErr.StatusCode >= 500 && apiErr.StatusCode <= 504:
			display.ShowHints("", []display.ActionHint{
				{Label: "Diagnose", Command: []string{"telara", "doctor"}, Description: "telara doctor"},
			})
		}
		return
	}

	display.PrintError(err.Error())
}

// friendlyAPIError converts an APIError into a user-friendly message.
func friendlyAPIError(e *api.APIError) string {
	switch e.StatusCode {
	case 401:
		return "Error: Token invalid or expired. Please sign in again with `telara login`."
	case 403:
		return "Error: You don't have permission to perform this action."
	case 404:
		return "Error: Resource not found."
	case 409:
		return "Error: Conflict — the resource already exists or is in an incompatible state."
	case 422:
		return "Error: Invalid request — " + e.Message
	case 429:
		return "Error: Too many requests. Please wait a moment and try again."
	case 500, 502, 503, 504:
		return "Error: The Telara API is currently unavailable. Please try again later."
	default:
		if e.Message != "" {
			return "Error: " + e.Message
		}
		return fmt.Sprintf("Error: Unexpected response from the API (HTTP %d).", e.StatusCode)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Telara API base URL (overrides config)")
	rootCmd.PersistentFlags().StringVar(&rootContext, "context", "", "Active context name (overrides config)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Print full HTTP response on errors")
	rootCmd.PersistentFlags().StringVar(&caCertPath, "ca-cert", "", "Path to CA certificate for TLS verification (local dev with self-signed cert)")
}

func initConfig() {
	var err error
	prefs, err = config.Load()
	if err != nil {
		// Not fatal — fall back to defaults so the CLI remains usable
		prefs = config.DefaultPrefs()
	}
	if rootAPIURL != "" {
		prefs.APIURL = rootAPIURL
	} else if envURL := os.Getenv("TELARA_API_URL"); envURL != "" {
		prefs.APIURL = envURL
	}
	if caCertPath != "" {
		os.Setenv("TELERA_CA_CERT_PATH", caCertPath)
	}
}
