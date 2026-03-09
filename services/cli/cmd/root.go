package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
)

var (
	rootAPIURL  string
	rootContext string
	verbose     bool
	prefs       *config.Prefs
)

// IsVerbose returns true when the --verbose flag is set.
func IsVerbose() bool { return verbose }

var rootCmd = &cobra.Command{
	Use:           "telara",
	Short:         "Telara CLI — manage your MCP configurations",
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
		fmt.Fprintln(os.Stderr, "Could not reach the Telara API. Check your connection and --api-url.")
		return
	}

	// API errors — show verbose body when requested.
	var apiErr *api.APIError
	if verbose && errors.As(err, &apiErr) {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", apiErr.StatusCode, apiErr.Body)
	}

	fmt.Fprintln(os.Stderr, "Error:", err)
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Telara API base URL (overrides config)")
	rootCmd.PersistentFlags().StringVar(&rootContext, "context", "", "Active context name (overrides config)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Print full HTTP response on errors")
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
	}
}
