package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/display"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show currently authenticated user",
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
			display.PrintKV(os.Stdout, "Context:", prefs.ActiveContext)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}
