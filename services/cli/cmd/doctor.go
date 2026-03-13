package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/clicontext"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

// checkResult holds the result of a single doctor check.
type checkResult struct {
	name    string
	status  string // "pass", "fail", "skip", "warn"
	message string
}

// runCheck wraps a check function with an animated spinner.
// It prints the result inline and returns it for summary counting.
func runCheck(label string, fn func() checkResult) checkResult {
	s := display.NewSpinner()
	s.Start(label + "...")
	r := fn()
	line := fmt.Sprintf("%-14s %s", r.name, r.message)
	switch r.status {
	case "pass":
		s.Success(line)
	case "fail":
		s.Fail(line)
	case "warn":
		s.Stop()
		display.PrintWarn(line)
	case "skip":
		s.Stop()
		fmt.Fprintf(os.Stderr, "  %s  %s\n", display.ColorDim.Sprint(display.IconDash), display.ColorDim.Sprint(line))
	default:
		s.Stop()
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}
	return r
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Verify your login, active connection, and editor configuration",
	Long: `Checks auth, API reachability, active context validity, installed editors,
and whether config files containing API keys are properly git-ignored.
Run this first when your AI tool isn't seeing your integrations or knowledge.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var results []checkResult

		results = append(results, runCheck("Checking auth", checkAuth))
		results = append(results, runCheck("Checking connectivity", func() checkResult {
			return checkConnectivity(prefs.APIURL)
		}))

		// Per-tool agent checks with individual spinners.
		home, homeErr := os.UserHomeDir()
		for _, tool := range agentToolDefs {
			tool := tool // capture
			results = append(results, runCheck("Checking "+tool.name, func() checkResult {
				if homeErr != nil {
					return checkResult{name: tool.name, status: "fail", message: "cannot determine home directory"}
				}
				return checkAgentTool(home, tool)
			}))
		}

		results = append(results, runCheck("Checking context", checkActiveContext))
		results = append(results, runCheck("Checking gitignore", checkGitignore))

		// Summary.
		passes, failures, skips, warns := 0, 0, 0, 0
		authFailed := false
		for _, r := range results {
			switch r.status {
			case "pass":
				passes++
			case "fail":
				failures++
				if r.name == "auth" {
					authFailed = true
				}
			case "skip":
				skips++
			case "warn":
				warns++
			}
		}

		fmt.Fprintln(os.Stderr)
		summary := fmt.Sprintf("%d passed, %d skipped, %d issues found", passes, skips+warns, failures)
		if failures == 0 && warns == 0 {
			display.PrintSuccess(summary)
		} else {
			display.PrintError(summary)
		}

		if authFailed {
			display.ShowHints("", []display.ActionHint{
				{Label: "Sign in", Command: []string{"telara", "login"}, Description: "telara login"},
			})
		}
		return nil
	},
}

// checkAuth validates the stored auth token.
func checkAuth() checkResult {
	token, err := auth.LoadToken(prefs.APIURL)
	if err != nil {
		return checkResult{
			name:    "auth",
			status:  "fail",
			message: "Not logged in. Run: telara login --token <tlrc_...>",
		}
	}

	// Call /v1/cli/auth/validate to verify the token.
	client := api.NewClient(prefs.APIURL, token)
	me, err := client.ValidateToken(context.Background())
	if err != nil {
		return checkResult{
			name:    "auth",
			status:  "fail",
			message: fmt.Sprintf("Token invalid or expired: %v", err),
		}
	}

	msg := fmt.Sprintf("Authenticated as %s", me.Email)
	return checkResult{name: "auth", status: "pass", message: msg}
}

// checkConnectivity does a quick GET to <apiURL>/health.
func checkConnectivity(apiURL string) checkResult {
	hc := &http.Client{Timeout: 5 * time.Second}
	healthURL := strings.TrimRight(apiURL, "/") + "/health"

	start := time.Now()
	resp, err := hc.Get(healthURL) //nolint:noctx
	elapsed := time.Since(start)

	host := apiURL
	if u := strings.TrimPrefix(apiURL, "https://"); u != apiURL {
		host = strings.Split(u, "/")[0]
	} else if u := strings.TrimPrefix(apiURL, "http://"); u != apiURL {
		host = strings.Split(u, "/")[0]
	}

	if err != nil {
		return checkResult{
			name:    "connectivity",
			status:  "fail",
			message: fmt.Sprintf("%s unreachable: %v", host, err),
		}
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return checkResult{
			name:    "connectivity",
			status:  "fail",
			message: fmt.Sprintf("%s returned %d", host, resp.StatusCode),
		}
	}

	return checkResult{
		name:    "connectivity",
		status:  "pass",
		message: fmt.Sprintf("%s reachable (%dms)", host, elapsed.Milliseconds()),
	}
}

// agentToolDef describes a supported agent tool for doctor checks.
type agentToolDef struct {
	name       string
	configDir  string // relative to home, e.g. ".claude"
	settingRel string // settings file path relative to configDir
}

var agentToolDefs = []agentToolDef{
	{
		name:       "claude-code",
		configDir:  ".claude",
		settingRel: "settings.json",
	},
	{
		name:       "cursor",
		configDir:  ".cursor",
		settingRel: "mcp.json",
	},
	{
		name:       "windsurf",
		configDir:  filepath.Join(".codeium", "windsurf"),
		settingRel: "mcp_config.json",
	},
	{
		name:       "vscode",
		configDir:  ".vscode",
		settingRel: "mcp.json",
	},
}

// checkAgentTool checks a single agent tool installation.
func checkAgentTool(home string, tool agentToolDef) checkResult {
	toolDir := filepath.Join(home, tool.configDir)
	if _, err := os.Stat(toolDir); os.IsNotExist(err) {
		return checkResult{
			name:    tool.name,
			status:  "skip",
			message: "not installed",
		}
	}

	settingsPath := filepath.Join(toolDir, tool.settingRel)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return checkResult{
			name:    tool.name,
			status:  "warn",
			message: fmt.Sprintf("installed, settings file not found (%s)", tool.settingRel),
		}
	}

	if strings.Contains(strings.ToLower(string(data)), "telara") {
		return checkResult{
			name:    tool.name,
			status:  "pass",
			message: "installed, telara configured",
		}
	}
	return checkResult{
		name:    tool.name,
		status:  "warn",
		message: "installed, no telara entry found in settings",
	}
}

// checkAgentTools runs all agent tool checks and returns the results.
func checkAgentTools() []checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return []checkResult{{name: "agent-tools", status: "fail", message: "cannot determine home directory"}}
	}
	var results []checkResult
	for _, tool := range agentToolDefs {
		results = append(results, checkAgentTool(home, tool))
	}
	return results
}

// checkActiveContext verifies the active context and its API key.
func checkActiveContext() checkResult {
	name := clicontext.Resolve(rootContext, prefs.ActiveContext)
	if name == "" {
		return checkResult{
			name:    "context",
			status:  "skip",
			message: "no active context set",
		}
	}

	store, err := newContextStore()
	if err != nil {
		return checkResult{
			name:    "context",
			status:  "fail",
			message: fmt.Sprintf("open context store: %v", err),
		}
	}
	c, err := store.Get(name)
	if err != nil {
		return checkResult{
			name:    "context",
			status:  "fail",
			message: fmt.Sprintf("context %q not found in store", name),
		}
	}

	// Try to verify the key is not revoked.
	token, err := auth.LoadToken(prefs.APIURL)
	if err == nil {
		client := api.NewClient(prefs.APIURL, token)
		keysResp, err := client.ListKeys(context.Background(), c.ConfigID)
		if err == nil {
			for _, k := range keysResp.Keys {
				if k.ID == c.APIKeyID {
					if k.Revoked {
						return checkResult{
							name:    "context",
							status:  "fail",
							message: fmt.Sprintf("active: %s (config: %s) — key is REVOKED", c.Name, c.ConfigName),
						}
					}
					break
				}
			}
		}
	}

	return checkResult{
		name:    "context",
		status:  "pass",
		message: fmt.Sprintf("active: %s (config: %s)", c.Name, c.ConfigName),
	}
}

// checkGitignore warns if any MCP settings files exist in the current
// directory but are not covered by .gitignore.
func checkGitignore() checkResult {
	sensitiveFiles := []string{
		".claude/settings.json",
		".cursor/mcp.json",
		".vscode/mcp.json",
		".windsurf/mcp_config.json",
	}

	// Find which sensitive files exist locally.
	var present []string
	for _, f := range sensitiveFiles {
		if _, err := os.Stat(f); err == nil {
			present = append(present, f)
		}
	}
	if len(present) == 0 {
		return checkResult{
			name:    "gitignore",
			status:  "pass",
			message: "no exposed keys in current directory",
		}
	}

	// Load .gitignore patterns (simple substring match is sufficient for common cases).
	ignored := loadGitignorePatterns(".gitignore")

	var exposed []string
	for _, f := range present {
		if !isIgnored(f, ignored) {
			exposed = append(exposed, f)
		}
	}

	if len(exposed) == 0 {
		return checkResult{
			name:    "gitignore",
			status:  "pass",
			message: "no exposed keys in current directory",
		}
	}

	return checkResult{
		name:    "gitignore",
		status:  "warn",
		message: fmt.Sprintf("these files may expose keys and are not in .gitignore: %s", strings.Join(exposed, ", ")),
	}
}

// loadGitignorePatterns reads a .gitignore file and returns its non-comment lines.
func loadGitignorePatterns(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// isIgnored reports whether path is covered by any of the gitignore patterns
// using a simple substring / suffix match.
func isIgnored(path string, patterns []string) bool {
	for _, p := range patterns {
		// Strip leading slash for comparison.
		p = strings.TrimPrefix(p, "/")
		if p == path || strings.HasSuffix(path, p) || strings.HasSuffix(path, "/"+p) {
			return true
		}
		// Also check just the directory component.
		dir := filepath.Dir(path)
		if p == dir || strings.HasSuffix(dir, "/"+p) {
			return true
		}
	}
	return false
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
