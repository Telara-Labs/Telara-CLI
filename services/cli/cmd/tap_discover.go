package cmd

// tap_discover.go is `telara tap discover` (TENG-3054, TENG-3059): the TAP
// runner's `tap discover` (gitlab.com/telara-labs/tap-runtime/discover), plus
// what only a Telara user has: saving through the same installer as
// `telara tap pull`, and publishing to the tenant's registry. Discovery reads
// local files and sends nothing; only an explicit publish talks to Telara
// (telara-documentation architecture/tap/24-what-we-can-observe.md section 1).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/tap-runtime/discover"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

var tapDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Find recurring work in this machine's agent session history",
	Long: `Read the session history Claude Code, Codex and Cursor keep on this machine,
split it into requests (each of your messages and the work it caused), and
report the requests that recur and pass every check as TAP primitives:
reviewed N sessions and M tool calls, found R routines, consolidated to P
primitives. --review lets you pick which to save or publish.

Everything happens locally. Nothing is uploaded unless you publish, and no
history of runs is kept: each run re-reads what the clients themselves keep.
Claude Code keeps about 30 days unless its cleanupPeriodDays setting is raised.`,
	Args: cobra.NoArgs,
	RunE: runTapDiscover,
}

func init() {
	d := discover.DefaultOptions()
	f := tapDiscoverCmd.Flags()
	f.StringSlice("client", []string{"claude-code", "codex", "cursor"}, "Clients to read")
	f.Int("days", 0, "Only sessions from the last N days (0 = all retained history)")
	f.Int("top", 25, "Candidates to print")
	f.Bool("json", false, "Print the full report as JSON")
	f.String("out", "", "Also write the JSON report to this file")
	f.Int("window", d.Window, "Most steps allowed between two steps of a pattern")
	f.Int("min-support", d.MinSupport, "Fewest sessions a pattern is mined at (bounds the search)")
	f.Int("max-len", d.MaxLen, "Longest pattern mined")
	f.Int("permutations", d.Permutations, "Shuffled corpora per null model")
	f.Int("max-patterns", d.MaxPatterns, "Most patterns kept for testing (a memory bound)")
	f.Float64("fdr", d.Alpha, "False discovery rate for 'beyond chance'")
	f.Int64("seed", d.Seed, "Seed for the shuffles, so a run can be repeated")
	f.Bool("review", false, "List the primitives found and pick which to save or publish")
	f.Bool("rejected", false, "Also list the routines each check removed, and why")
	f.Bool("patterns", false, "Also run the pattern search (slower; fragments, families, skill comparison)")
	f.String("publisher", "", "Publisher namespace for drafts you publish (reverse-DNS, e.g. com.acme)")
	f.String("install-client", "claude-code", "Where saved drafts are installed: an agent client, or 'all'")
	f.String("install-scope", "global", "Install saved drafts globally or into this project: global | project")
	tapCmd.AddCommand(tapDiscoverCmd)
}

func runTapDiscover(cmd *cobra.Command, _ []string) error {
	fl := cmd.Flags()
	o := discover.DefaultOptions()
	clients, _ := fl.GetStringSlice("client")
	days, _ := fl.GetInt("days")
	top, _ := fl.GetInt("top")
	asJSON, _ := fl.GetBool("json")
	out, _ := fl.GetString("out")
	o.Window, _ = fl.GetInt("window")
	o.MinSupport, _ = fl.GetInt("min-support")
	o.MaxLen, _ = fl.GetInt("max-len")
	o.Permutations, _ = fl.GetInt("permutations")
	o.MaxPatterns, _ = fl.GetInt("max-patterns")
	o.Alpha, _ = fl.GetFloat64("fdr")
	o.Seed, _ = fl.GetInt64("seed")
	o.Patterns, _ = fl.GetBool("patterns")
	o.Progress = cmd.ErrOrStderr()
	if days > 0 {
		o.Since = time.Now().AddDate(0, 0, -days)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	readers, err := discover.DefaultReaders(clients, home)
	if err != nil {
		return err
	}
	o.Readers = readers

	rep, err := discover.Run(o)
	if err != nil {
		return err
	}
	if out != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, b, 0o600); err != nil {
			return err
		}
	}
	if review, _ := fl.GetBool("review"); review {
		publisher, _ := fl.GetString("publisher")
		discover.WriteFunnel(cmd.OutOrStdout(), rep, top, false)
		return discover.Review(cmd.InOrStdin(), cmd.OutOrStdout(), rep, discover.ReviewConfig{Top: top, Publisher: publisher}, liveReviewActions(cmd))
	}
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	rejected, _ := fl.GetBool("rejected")
	discover.WriteFunnel(cmd.OutOrStdout(), rep, top, rejected)
	if o.Patterns {
		fmt.Fprintln(cmd.OutOrStdout())
		discover.WriteText(cmd.OutOrStdout(), rep, top)
	}
	return nil
}

// liveReviewActions saves by installing the package as `telara tap pull`
// does, and publishes through the Telara MCP tool telara_skill_publish.
func liveReviewActions(cmd *cobra.Command) discover.ReviewActions {
	clientName, _ := cmd.Flags().GetString("install-client")
	scopeName, _ := cmd.Flags().GetString("install-scope")
	return discover.ReviewActions{
		Save: func(d *discover.Draft) (string, error) {
			scope, err := parseSkillScope(scopeName)
			if err != nil {
				return "", err
			}
			pkg, digest, err := d.Package()
			if err != nil {
				return "", err
			}
			in := skillshare.PrimitiveInstall{Publisher: d.Publisher, Name: d.Name, Version: "0.1.0", ArtifactDigest: digest, Package: pkg}
			targets, err := installTargets(clientName, scope)
			if err != nil {
				return "", err
			}
			if len(targets) == 0 {
				return "", fmt.Errorf("no agent client with a skills directory was detected")
			}
			var paths []string
			for _, t := range targets {
				res, err := skillshare.InstallPrimitive(t.dir, in, false)
				if err != nil {
					return "", err
				}
				paths = append(paths, res.Path)
			}
			return strings.Join(paths, ", "), nil
		},
		CanPublish: func() string {
			if _, err := auth.LoadMCPKey(prefs.APIURL); err != nil {
				return "not signed in to Telara on this machine (run: telara login, then telara install). Saved drafts stay local."
			}
			return ""
		},
		Publish: func(d *discover.Draft, audience string) (string, bool, error) {
			key, err := auth.LoadMCPKey(prefs.APIURL)
			if err != nil {
				return "", false, err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			audienceID := ""
			if audience == "user" {
				client, err := tapClient()
				if err != nil {
					return "", false, err
				}
				who, err := client.ValidateToken(ctx)
				if err != nil {
					return "", false, fmt.Errorf("find your user id: %w", err)
				}
				audienceID = who.UserID
			}
			m := &mcpCaller{endpoint: streamableDefaultMCPURL(), key: key}
			if err := m.initialize(ctx); err != nil {
				return "", false, err
			}
			desc := strings.SplitN(string(d.Files["README.md"]), "\n\n", 3)
			summary := d.Name
			if len(desc) > 1 {
				summary = strings.TrimSpace(desc[1])
			}
			text, isError, err := m.callTool(ctx, "telara_skill_publish", publishArgs(d.Name, summary, d.Publisher, audience, audienceID, d.Files))
			return text, err == nil && !isError, err
		},
	}
}
