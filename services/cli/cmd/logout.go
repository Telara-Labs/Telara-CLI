package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/auth"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Revoke your CLI token and remove local credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			// Not logged in — clean up any local file silently, treat as success
			_ = auth.DeleteToken(prefs.APIURL)
			fmt.Fprintln(os.Stdout, "Logged out")
			return nil
		}

		client := api.NewClient(prefs.APIURL, token)
		if err := client.RevokeToken(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to revoke token server-side: %v\n", err)
			// Still delete locally
		}

		if err := auth.DeleteToken(prefs.APIURL); err != nil {
			return fmt.Errorf("failed to remove local token: %w", err)
		}

		fmt.Fprintln(os.Stdout, "Logged out")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}
