package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
)

var (
	installAll    bool
	installClient string
	installScope  string
	installYes    bool
	installDryRun bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Connect supported AI clients to Telara",
	Long: `Connects Telara to supported AI clients without requiring you to edit MCP files.
The command uses your Telara login to issue a user-bound credential and writes only
the selected client configuration. Use --dry-run to inspect every target first.`,
	Example: `  telara install
  telara install --client codex
  telara install --all --scope managed --yes
  telara install --dry-run`,
	RunE: runInstall,
}

type installResult struct {
	client string
	status string
	detail string
}

func init() {
	installCmd.Flags().BoolVar(&installAll, "all", false, "Configure every detected supported client (the default when --client is omitted)")
	installCmd.Flags().StringVar(&installClient, "client", "", "Configure one client (claude-code, cursor, windsurf, vscode, codex, gemini, amazon-q)")
	installCmd.Flags().StringVar(&installScope, "scope", "global", "Configuration scope: global or managed")
	installCmd.Flags().BoolVar(&installYes, "yes", false, "Confirm non-interactive installation")
	installCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "Show target files without writing or issuing a key")
	rootCmd.AddCommand(installCmd)
}

func runInstall(command *cobra.Command, _ []string) error {
	if installAll && installClient != "" {
		return fmt.Errorf("use either --all or --client, not both")
	}
	scope, err := parseInstallScope(installScope)
	if err != nil {
		return err
	}
	writers, err := installWriters(installClient)
	if err != nil {
		return err
	}

	if installDryRun {
		results := dryRunInstall(writers, scope)
		printInstallResults(command.OutOrStdout(), results)
		return nil
	}
	if !installYes {
		if err := confirmInstall(command.InOrStdin(), command.OutOrStdout(), writers, scope); err != nil {
			return err
		}
	}

	token, err := auth.LoadToken(prefs.APIURL)
	if err != nil {
		return fmt.Errorf("not logged in — run: telara login")
	}
	results, err := installWritersWithCredential(context.Background(), api.NewClient(prefs.APIURL, token), writers, scope)
	printInstallResults(command.OutOrStdout(), results)
	return err
}

func confirmInstall(in io.Reader, out io.Writer, writers []agent.AgentWriter, scope agent.Scope) error {
	clients := make([]string, 0, len(writers))
	for _, writer := range writers {
		clients = append(clients, writer.Name())
	}
	fmt.Fprintf(out, "Connect Telara to %s at %s scope? [y/N] ", strings.Join(clients, ", "), installScopeName(scope))
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return fmt.Errorf("installation needs confirmation; re-run with --yes for non-interactive use")
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("installation cancelled")
	}
	return nil
}

func installScopeName(scope agent.Scope) string {
	if scope == agent.ScopeManaged {
		return "managed"
	}
	return "global"
}

func parseInstallScope(value string) (agent.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "global":
		return agent.ScopeGlobal, nil
	case "managed":
		return agent.ScopeManaged, nil
	default:
		return 0, fmt.Errorf("unsupported install scope %q: use global or managed", value)
	}
}

func installWriters(name string) ([]agent.AgentWriter, error) {
	if name != "" {
		writer := agent.WriterByName(name)
		if writer == nil {
			return nil, fmt.Errorf("unknown client %q", name)
		}
		return []agent.AgentWriter{writer}, nil
	}
	writers := agent.DetectedWriters()
	if len(writers) == 0 {
		return nil, fmt.Errorf("no supported AI client was detected; use --client to select one explicitly")
	}
	return writers, nil
}

func dryRunInstall(writers []agent.AgentWriter, scope agent.Scope) []installResult {
	results := make([]installResult, 0, len(writers))
	for _, writer := range writers {
		path, err := writer.ConfigPath(scope)
		if err != nil {
			results = append(results, installResult{client: writer.Name(), status: "UNSUPPORTED", detail: err.Error()})
			continue
		}
		results = append(results, installResult{client: writer.Name(), status: "DRY RUN", detail: path})
	}
	return results
}

func installWritersWithCredential(ctx context.Context, client *api.Client, writers []agent.AgentWriter, scope agent.Scope) ([]installResult, error) {
	if len(writers) == 0 {
		return nil, fmt.Errorf("no clients selected")
	}
	rawKey, mcpURL, configName, err := onboardingCredential(ctx, client, toolKeyName(writers[0].Name()))
	if err != nil {
		return nil, fmt.Errorf("could not get a Telara credential: %w", err)
	}
	if mcpURL == "" {
		mcpURL = defaultMCPURL()
	}

	results := make([]installResult, 0, len(writers))
	var failed bool
	for _, writer := range writers {
		entry := newMCPEntryForWriter(mcpURL, rawKey, writer)
		if err := writer.Write(scope, "telara", entry); err != nil {
			results = append(results, installResult{client: writer.Name(), status: "FAILED", detail: err.Error()})
			failed = true
			continue
		}
		if permissions, ok := writer.(agent.PermissionWriter); ok {
			if err := permissions.WritePermissions(scope, "telara"); err != nil {
				results = append(results, installResult{client: writer.Name(), status: "FAILED", detail: fmt.Sprintf("MCP entry written, but permissions failed: %v", err)})
				failed = true
				continue
			}
		}
		results = append(results, installResult{client: writer.Name(), status: "CONNECTED", detail: configName})
	}
	if failed {
		return results, fmt.Errorf("one or more clients could not be configured")
	}
	return results, nil
}

func printInstallResults(out io.Writer, results []installResult) {
	fmt.Fprintln(out, "CLIENT\tSTATUS\tDETAIL")
	for _, result := range results {
		fmt.Fprintf(out, "%s\t%s\t%s\n", result.client, result.status, result.detail)
	}
}
