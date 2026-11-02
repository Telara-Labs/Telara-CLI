package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/handoffrunner"
)

var (
	handoffRunnerDir      string
	handoffRunnerCodexBin string
	handoffRunnerMCPURL   string
	handoffRunnerOnce     bool
	handoffRunnerInterval time.Duration
	handoffRunnerBeat     time.Duration
)

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Work handed to you through Telara",
}

var handoffRunnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Run work you accepted for your own Codex, on this machine",
	Long: `The runner starts your own Codex for handoffs you accepted for it, on this
machine, under your own Telara login and your own Codex login. Each run gets a
fresh thread whose only tool connection is that work's grant, in an empty working
directory with no network, and it stops when Telara says stop or the grant ends.

Set up once:
  telara handoff runner setup
  telara handoff runner login     sign in to Codex inside the runner's own home

Then:
  telara handoff runner run`,
}

func handoffRunner() (*handoffrunner.Runner, error) {
	dir := handoffRunnerDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".telara", "handoff-runner")
	}
	mcpURL := handoffRunnerMCPURL
	if mcpURL == "" {
		mcpURL = streamableDefaultMCPURL()
	}
	return &handoffrunner.Runner{CodexBin: handoffRunnerCodexBin, Dir: dir, MCPURL: mcpURL, Heartbeat: handoffRunnerBeat, Log: os.Stderr}, nil
}

var handoffRunnerSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Create the runner's private directories",
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := handoffRunner()
		if err != nil {
			return err
		}
		if err := r.PrepareHome(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Runner directory: %s\nNext, sign in to Codex inside it: telara handoff runner login\n", r.Dir)
		return nil
	},
}

var handoffRunnerLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to Codex inside the runner's own isolated home",
	Long: `Runs "codex login" with the runner's own Codex home, so the runner uses your
Codex account without your everyday Codex configuration. Your Codex login stays
on this machine; Telara never receives it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := handoffRunner()
		if err != nil {
			return err
		}
		if err := r.PrepareHome(); err != nil {
			return err
		}
		login := exec.Command(handoffRunnerCodexBin, "login")
		login.Stdin, login.Stdout, login.Stderr = os.Stdin, os.Stdout, os.Stderr
		// CODEX_HOME is Codex's own variable, set for this child process only.
		login.Env = append(os.Environ(), "CODEX_HOME="+r.CodexHome())
		return login.Run()
	},
}

var handoffRunnerRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Start your own Codex for the handoffs you accepted for it",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			return fmt.Errorf("not logged in — run: telara login")
		}
		r, err := handoffRunner()
		if err != nil {
			return err
		}
		if err := r.PrepareHome(); err != nil {
			return err
		}
		client := api.NewClient(prefs.APIURL, token)
		client.HTTPClient.Timeout = 60 * time.Second
		r.Control = client
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if handoffRunnerOnce {
			return r.Once(ctx)
		}
		return r.Loop(ctx, handoffRunnerInterval)
	},
}

func init() {
	for _, c := range []*cobra.Command{handoffRunnerSetupCmd, handoffRunnerLoginCmd, handoffRunnerRunCmd} {
		c.Flags().StringVar(&handoffRunnerDir, "dir", "", "the runner's own directory (default ~/.telara/handoff-runner)")
		c.Flags().StringVar(&handoffRunnerCodexBin, "codex-bin", "codex", "the codex program")
	}
	handoffRunnerRunCmd.Flags().StringVar(&handoffRunnerMCPURL, "mcp-url", "", "Telara MCP endpoint the grant session attaches to (default from the API URL)")
	handoffRunnerRunCmd.Flags().BoolVar(&handoffRunnerOnce, "once", false, "serve the waiting runs and exit")
	handoffRunnerRunCmd.Flags().DurationVar(&handoffRunnerInterval, "interval", 15*time.Second, "how often to look for new runs")
	handoffRunnerRunCmd.Flags().DurationVar(&handoffRunnerBeat, "heartbeat", 5*time.Second, "how often a running run is reported, and so how soon a stop lands")
	handoffRunnerCmd.AddCommand(handoffRunnerSetupCmd, handoffRunnerLoginCmd, handoffRunnerRunCmd)
	handoffCmd.AddCommand(handoffRunnerCmd)
	rootCmd.AddCommand(handoffCmd)
}
