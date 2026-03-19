package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/clicontext"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Submit a bug report to the Telara team",
	Long: `Submit a bug report directly from the CLI.
If authenticated, your user details are included automatically.
Otherwise you will be prompted for your name and email.`,
	RunE: runReport,
}

func init() {
	reportCmd.Flags().StringP("title", "t", "", "Bug title (required)")
	reportCmd.Flags().StringP("severity", "s", "medium", "Severity: low, medium, high, critical")
	reportCmd.Flags().StringP("description", "d", "", "Bug description")
	rootCmd.AddCommand(reportCmd)
}

func runReport(cmd *cobra.Command, args []string) error {
	title, _ := cmd.Flags().GetString("title")
	severity, _ := cmd.Flags().GetString("severity")
	description, _ := cmd.Flags().GetString("description")

	reader := bufio.NewReader(os.Stdin)

	if title == "" {
		fmt.Fprint(os.Stderr, "Bug title: ")
		line, _ := reader.ReadString('\n')
		title = strings.TrimSpace(line)
		if title == "" {
			return fmt.Errorf("title is required")
		}
	}

	if description == "" {
		fmt.Fprint(os.Stderr, "Description: ")
		line, _ := reader.ReadString('\n')
		description = strings.TrimSpace(line)
		if description == "" {
			return fmt.Errorf("description is required")
		}
	}

	// Build environment info
	environment := map[string]string{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
	}
	ctxName := clicontext.Resolve(rootContext, prefs.ActiveContext)
	if ctxName != "" {
		environment["context"] = ctxName
	}
	environment["api_url"] = prefs.APIURL

	// Determine auth state
	var (
		reporterEmail string
		reporterName  string
		userID        string
		tenantID      string
		client        *api.Client
	)

	token, err := auth.LoadToken(prefs.APIURL)
	if err == nil {
		client = api.NewClient(prefs.APIURL, token)
		me, verr := client.ValidateToken(context.Background())
		if verr == nil {
			reporterEmail = me.Email
			reporterName = me.DisplayName
			userID = me.UserID
			tenantID = me.TenantID
		}
	}

	if reporterEmail == "" {
		fmt.Fprint(os.Stderr, "Your email: ")
		line, _ := reader.ReadString('\n')
		reporterEmail = strings.TrimSpace(line)
		if reporterEmail == "" {
			return fmt.Errorf("email is required")
		}
	}
	if reporterName == "" {
		fmt.Fprint(os.Stderr, "Your name: ")
		line, _ := reader.ReadString('\n')
		reporterName = strings.TrimSpace(line)
	}

	if client == nil {
		client = api.NewClient(prefs.APIURL, "")
	}

	body := map[string]interface{}{
		"reporterEmail": reporterEmail,
		"reporterName":  reporterName,
		"title":         title,
		"description":   description,
		"severity":      severity,
		"source":        "cli",
		"environment":   environment,
		"userId":        userID,
		"tenantId":      tenantID,
	}

	s := display.NewSpinner()
	s.Start("Submitting bug report...")

	var resp api.BugReportResponse
	if err := client.Post(context.Background(), "/v1/notifications/bug-report", body, &resp); err != nil {
		s.Fail("Failed to submit bug report")
		return err
	}

	s.Success(fmt.Sprintf("Bug report submitted! Reference: %s", resp.ReportID))
	return nil
}
