package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/mcpstdio"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/version"
)

// mcp.go is the local MCP shim entry point (TENG-3019).
//
// A client configured against the HTTP endpoint has no per-chat identity: MCP
// 2026-07-28 removed the session id, so every chat on one API key shares the
// gateway's single `stateless:<tenant>:<user>:<config>` strand and Observability
// Sessions cannot tell two chats apart. Run as a stdio server instead, one
// process per chat, and the process mints the conversation handle that makes its
// chat a session of its own. See internal/mcpstdio for why this is the fix and
// not a workaround.

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run Telara as a local MCP server",
}

var mcpStdioCmd = &cobra.Command{
	Use:   "stdio",
	Short: "Serve MCP over stdio, giving this chat its own session",
	Long: `Serve the Telara MCP endpoint over stdio, for a client that launches its MCP
servers as commands.

Each run of this command is one conversation. It mints a handle at startup and
sends it with every request, so the chat it serves appears as its own session in
Observability rather than merging into every other chat on the same API key.

Configure a client to launch it, for example with Claude Code:

    claude mcp add telara -- telara mcp stdio

Speaks on stdin and stdout: run it from a client, not by hand.`,
	Args: cobra.NoArgs,
	RunE: runMCPStdio,
}

func runMCPStdio(cmd *cobra.Command, _ []string) error {
	// Cobra prints usage on any returned error, which would write help text into
	// the client's JSON-RPC stream. Everything below reports through stderr.
	cmd.SilenceUsage = true

	endpoint := streamableDefaultMCPURL()
	if flagURL, _ := cmd.Flags().GetString("endpoint"); strings.TrimSpace(flagURL) != "" {
		endpoint = strings.TrimSpace(flagURL)
	}

	apiKey, err := resolveMCPKey(cmd)
	if err != nil {
		return err
	}

	shim, err := mcpstdio.New(endpoint, apiKey, version.Version)
	if err != nil {
		return err
	}
	shim.HTTP = mcpstdio.DefaultHTTPClient()

	// A client stops its MCP server by closing stdin or signalling it. Both end
	// the run cleanly; neither is an error to report.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "telara mcp: serving %s as conversation %s\n", endpoint, shim.Conversation)

	if err := shim.Run(ctx, os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// resolveMCPKey finds the key this machine already holds for the endpoint.
//
// Deliberately not an environment variable and not a positional argument: the
// first is invisible configuration that drifts from what is deployed, and the
// second would put a live credential in every `ps` listing on the machine. The
// key lives in the same 0600 per-host credential file the login token uses.
func resolveMCPKey(cmd *cobra.Command) (string, error) {
	if flagKey, _ := cmd.Flags().GetString("key-file"); strings.TrimSpace(flagKey) != "" {
		data, err := os.ReadFile(strings.TrimSpace(flagKey))
		if err != nil {
			return "", fmt.Errorf("read --key-file: %w", err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("--key-file %s is empty", flagKey)
		}
		return key, nil
	}

	key, err := auth.LoadMCPKey(prefs.APIURL)
	if errors.Is(err, auth.ErrNoMCPKey) {
		return "", fmt.Errorf("no MCP key stored for %s — run `telara install` to create one", prefs.APIURL)
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

var mcpSetKeyCmd = &cobra.Command{
	Use:   "set-key",
	Short: "Store the MCP API key that `telara mcp stdio` will use",
	Long: `Read an MCP API key on stdin and store it for this machine.

The key is written to the same 0600 per-host credential file as the login token.
Reading it on stdin rather than as an argument keeps it out of shell history and
out of every 'ps' listing on the machine.

    pbpaste | telara mcp set-key
    telara mcp set-key < key.txt`,
	Args: cobra.NoArgs,
	RunE: runMCPSetKey,
}

func runMCPSetKey(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true

	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return fmt.Errorf("read key from stdin: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return errors.New("no key on stdin — pipe the MCP API key in, for example: pbpaste | telara mcp set-key")
	}
	if err := auth.SaveMCPKey(prefs.APIURL, key); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Stored the MCP key for %s.\n", prefs.APIURL)
	return nil
}

func init() {
	mcpStdioCmd.Flags().String("endpoint", "", "Override the MCP endpoint (defaults to the configured API URL)")
	mcpStdioCmd.Flags().String("key-file", "", "Read the MCP API key from this file instead of the stored credentials")

	mcpCmd.AddCommand(mcpStdioCmd, mcpSetKeyCmd)
	rootCmd.AddCommand(mcpCmd)
}
