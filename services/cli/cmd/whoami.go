package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/auth"
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

		fmt.Fprintf(os.Stdout, "User:         %s\n", whoami.DisplayName)
		fmt.Fprintf(os.Stdout, "Email:        %s\n", whoami.Email)
		fmt.Fprintf(os.Stdout, "Organization: %s\n", whoami.OrgName)
		fmt.Fprintf(os.Stdout, "Token prefix: %s\n", whoami.TokenPrefix)
		if prefs.ActiveContext != "" {
			fmt.Fprintf(os.Stdout, "Context:      %s\n", prefs.ActiveContext)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}
