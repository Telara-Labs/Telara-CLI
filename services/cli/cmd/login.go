package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
)

var loginToken string

var loginCmd = &cobra.Command{
	Use:     "login",
	Short:   "Authenticate with the Telara API using a CLI token",
	Example: "  telara login --token tlrc_abc123...",
	RunE: func(cmd *cobra.Command, args []string) error {
		if loginToken == "" {
			return fmt.Errorf("--token is required\n\nGenerate a token at: https://app.telara.ai/settings/developer")
		}
		if err := auth.ValidateTokenFormat(loginToken); err != nil {
			return err
		}

		client := api.NewClient(prefs.APIURL, loginToken)
		whoami, err := client.ValidateToken(context.Background())
		if err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}

		if err := auth.SaveToken(prefs.APIURL, loginToken); err != nil {
			return fmt.Errorf("failed to save token: %w", err)
		}

		fmt.Fprintf(os.Stdout, "Authenticated as %s (%s)\n", whoami.Email, whoami.OrgName)
		return nil
	},
}

func init() {
	loginCmd.Flags().StringVarP(&loginToken, "token", "t", "", "CLI token (tlrc_...)")
	rootCmd.AddCommand(loginCmd)
}
