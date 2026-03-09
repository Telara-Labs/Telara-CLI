package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/config"
)

var (
	rootAPIURL string
	prefs      *config.Prefs
)

var rootCmd = &cobra.Command{
	Use:           "telara",
	Short:         "Telara CLI — manage your MCP configurations",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute is the entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&rootAPIURL, "api-url", "", "Telara API base URL (overrides config)")
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
