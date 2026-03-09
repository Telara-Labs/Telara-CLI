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
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/clicontext"
)

// checkResult holds the result of a single doctor check.
type checkResult struct {
	name    string
	status  string // "pass", "fail", "skip", "warn"
	message string
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check your Telara CLI configuration and environment",
	RunE: func(cmd *cobra.Command, args []string) error {
		var results []checkResult

		// ------------------------------------------------------------------
		// 1. Auth check
		// ------------------------------------------------------------------
		results = append(results, checkAuth())

		// ------------------------------------------------------------------
		// 2. Connectivity check
		// ------------------------------------------------------------------
		results = append(results, checkConnectivity(prefs.APIURL))

		// ------------------------------------------------------------------
		// 3. Agent tool checks
		// ------------------------------------------------------------------
		results = append(results, checkAgentTools()...)

		// ------------------------------------------------------------------
		// 4. Active context check
		// ------------------------------------------------------------------
		results = append(results, checkActiveContext())

		// ------------------------------------------------------------------
		// 5. .gitignore check
		// ------------------------------------------------------------------
		results = append(results, checkGitignore())

		// ------------------------------------------------------------------
		// Print results
		// ------------------------------------------------------------------
		passes, failures, skips, warns := 0, 0, 0, 0
		for _, r := range results {
			var marker string
			switch r.status {
			case "pass":
				marker = "checkmark"
				passes++
			case "fail":
				marker = "FAIL"
				failures++
			case "skip":
				marker = "-"
				skips++
			case "warn":
				marker = "WARN"
				warns++
			default:
				marker = "?"
			}
			// Replace "checkmark" with the unicode check character.
			if marker == "checkmark" {
				marker = "\u2713"
			}
			fmt.Fprintf(os.Stdout, "  %-14s %s %s\n", r.name, marker, r.message)
		}

		fmt.Fprintln(os.Stdout, "")
		summary := fmt.Sprintf("%d skipped, %d issues found", skips+warns, failures)
		if failures == 0 && warns == 0 {
			fmt.Fprintln(os.Stdout, summary)
		} else {
			fmt.Fprintln(os.Stdout, summary)
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

func checkAgentTools() []checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return []checkResult{{name: "agent-tools", status: "fail", message: "cannot determine home directory"}}
	}

	var results []checkResult
	for _, tool := range agentToolDefs {
		toolDir := filepath.Join(home, tool.configDir)
		if _, err := os.Stat(toolDir); os.IsNotExist(err) {
			results = append(results, checkResult{
				name:    tool.name,
				status:  "skip",
				message: "not installed",
			})
			continue
		}

		// Tool directory exists — check if the settings file mentions "telara".
		settingsPath := filepath.Join(toolDir, tool.settingRel)
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			results = append(results, checkResult{
				name:    tool.name,
				status:  "warn",
				message: fmt.Sprintf("installed, settings file not found (%s)", tool.settingRel),
			})
			continue
		}

		if strings.Contains(strings.ToLower(string(data)), "telara") {
			results = append(results, checkResult{
				name:    tool.name,
				status:  "pass",
				message: "installed, telara configured",
			})
		} else {
			results = append(results, checkResult{
				name:    tool.name,
				status:  "warn",
				message: "installed, no telara entry found in settings",
			})
		}
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
