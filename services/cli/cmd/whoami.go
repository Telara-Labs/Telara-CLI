package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show who you are logged in as and which organization you belong to",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
		}

		client := api.NewClient(prefs.APIURL, token)
		whoami, err := client.ValidateToken(context.Background())
		if err != nil {
			return fmt.Errorf("failed to verify token: %w\n\nRun: telara login --token <tlrc_...>", err)
		}

		display.PrintKV(os.Stdout, "User:", whoami.DisplayName)
		display.PrintKV(os.Stdout, "Email:", whoami.Email)
		display.PrintKV(os.Stdout, "Organization:", whoami.OrgName)
		display.PrintKV(os.Stdout, "Token prefix:", whoami.TokenPrefix)
		if prefs.ActiveContext != "" {
			display.PrintKV(os.Stdout, "Global config:", prefs.ActiveContext)
		}
		wiredState, _ := agent.LoadWiredState()
		if wiredState != nil && wiredState.Projects != nil {
			if cwd, err := os.Getwd(); err == nil {
				if wc, ok := wiredState.Projects[cwd]; ok && wc != nil {
					display.PrintKV(os.Stdout, "Project config:", wc.ConfigName)
				}
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}
