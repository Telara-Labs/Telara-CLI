package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

// skill.go is the consent-gated share path (TENG-1998).
//
// Separate command tree from `telara scan` deliberately. Scan is unattended,
// scheduled daily, and never reads a skill body. Share is interactive,
// per-skill, and uploads the body on purpose. Folding share into scan would put
// a body upload on a cron.

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Share and manage agent skills",
	Long: `Share a locally installed agent skill with your team, your enterprise, or publicly.

Sharing uploads the skill's CONTENT, unlike 'telara scan', which only ever reports
that a skill exists. Every share runs a secret scan first, on this machine, before
anything is sent.`,
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List skills installed on this machine and skills shared with you",
	RunE:  runSkillList,
}

var skillShareCmd = &cobra.Command{
	Use:   "share <skill-name>",
	Short: "Share a locally installed skill",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillShare,
}

var skillRevokeCmd = &cobra.Command{
	Use:   "revoke <skill-id>",
	Short: "Withdraw a shared skill",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillRevoke,
}

func init() {
	skillShareCmd.Flags().String("scope", "", "Who receives it: team | enterprise | open-source (required)")
	skillShareCmd.Flags().Bool("yes", false, "Skip the interactive prompt (still refuses on critical findings unless --force)")
	skillShareCmd.Flags().Bool("force", false, "Skip the LOCAL scan check. The server re-scans and may still refuse")
	skillShareCmd.Flags().Bool("dry-run", false, "Run the scan and print what would be sent, without sending it")

	skillCmd.AddCommand(skillListCmd, skillShareCmd, skillRevokeCmd)
	rootCmd.AddCommand(skillCmd)
}

// localSkillsRoot is the user-global skills directory, matching the discovery
// scanner's global scope so `skill share` and `scan` agree on what is installed.
func localSkillsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

func runSkillList(cmd *cobra.Command, args []string) error {
	root, err := localSkillsRoot()
	if err != nil {
		return err
	}

	fmt.Println("Installed on this machine:")
	local := discovery.ScanSkills()
	var any bool
	for _, r := range local {
		for _, s := range r.Skills {
			any = true
			fmt.Printf("  %-28s %s\n", s.SkillName, shortHash(s.ContentHash))
		}
	}
	if !any {
		fmt.Printf("  (none found under %s)\n", root)
	}

	endpoint := config.ScanSubmitEndpoint()
	token, err := auth.LoadToken(endpoint)
	if err != nil {
		fmt.Println("\nNot logged in — run: telara login --token <tlrc_...> to see shared skills.")
		return nil
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	resp, err := api.NewClient(endpoint, token).ListSharedSkills(ctx)
	if err != nil {
		return fmt.Errorf("list shared skills: %w", err)
	}

	fmt.Println("\nShared with you:")
	if len(resp.Skills) == 0 {
		fmt.Println("  (none)")
		return nil
	}
	sort.Slice(resp.Skills, func(i, j int) bool { return resp.Skills[i].Name < resp.Skills[j].Name })
	for _, s := range resp.Skills {
		state := ""
		if s.Revoked {
			state = "  [revoked]"
		}
		fmt.Printf("  %-28s v%-3d %-11s %s%s\n", s.Name, s.Version, s.Scope, s.SkillID, state)
	}
	return nil
}

func runSkillShare(cmd *cobra.Command, args []string) error {
	name := args[0]
	scopeRaw, _ := cmd.Flags().GetString("scope")
	assumeYes, _ := cmd.Flags().GetBool("yes")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// No default scope: the audience is the decision being made, and defaulting
	// it either way hides the most consequential field in the command.
	if strings.TrimSpace(scopeRaw) == "" {
		return fmt.Errorf("--scope is required (team | enterprise | open-source)")
	}
	scope, err := skillshare.ParseScope(scopeRaw)
	if err != nil {
		return err
	}

	root, err := localSkillsRoot()
	if err != nil {
		return err
	}
	skill, err := skillshare.LoadSkill(root, name)
	if err != nil {
		return err
	}

	req, findings := skillshare.BuildRequest(skill, scope, false)

	fmt.Printf("Skill:   %s\n", skill.Name)
	if skill.Version != "" {
		fmt.Printf("Version: %s\n", skill.Version)
	}
	fmt.Printf("Hash:    %s\n", shortHash(skill.ContentHash))
	fmt.Printf("Scope:   %s\n", scope)
	fmt.Printf("Source:  %s\n\n", skill.Path)

	verdict := skillshare.LocalVerdict(skill.Body)
	fmt.Printf("Scanning for secrets... %s\n", pluraliseFindings(len(findings)))
	for _, f := range findings {
		marker := "  ·"
		if f.Severity == skillshare.SeverityCritical {
			marker = "  !"
		}
		fmt.Printf("%s line %d: %s (%s)\n", marker, f.Line, f.Excerpt, f.Category)
	}
	if len(findings) > 0 {
		fmt.Printf("\nRisk %d (threshold %d, %s)\n", verdict.Score, verdict.Threshold, verdict.PolicyVersion)
		// The server re-scans and decides. Saying so here keeps the CLI from
		// promising an outcome it does not control.
		if verdict.Blocked {
			fmt.Println("This exceeds the blocking threshold; the server will refuse it.")
		}
		fmt.Println()
	}

	if err := shareGate(findings, force); err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("Dry run — %d bytes would be uploaded. Nothing was sent.\n", len(req.Body))
		return nil
	}

	if scope.IsIrreversible() {
		fmt.Println("WARNING: open-source sharing cannot be undone. Once published the content")
		fmt.Println("can be crawled, forked and indexed; revoking only stops Telara serving it.")
		if !confirm(fmt.Sprintf("Publish %q publicly?", skill.Name), assumeYes && force) {
			return fmt.Errorf("aborted")
		}
	} else if !assumeYes {
		if !confirm(fmt.Sprintf("Share %q with %s?", skill.Name, scope), false) {
			return fmt.Errorf("aborted")
		}
	}

	// Recorded only once a human has actually accepted the findings, so the
	// server stores a decision with an owner rather than a default.
	req.AcknowledgedRisk = len(findings) > 0

	endpoint := config.ScanSubmitEndpoint()
	token, err := auth.LoadToken(endpoint)
	if err != nil {
		return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	resp, err := api.NewClient(endpoint, token).ShareSkill(ctx, req)
	if err != nil {
		return fmt.Errorf("share skill: %w", err)
	}

	verb := "shared"
	if resp.Superseded {
		verb = "updated"
	}
	fmt.Printf("\n%s %s as v%d (%s), visible to %s\n", skill.Name, verb, resp.Version, resp.SkillID, resp.Scope)
	return nil
}

func runSkillRevoke(cmd *cobra.Command, args []string) error {
	skillID := args[0]
	endpoint := config.ScanSubmitEndpoint()
	token, err := auth.LoadToken(endpoint)
	if err != nil {
		return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	if err := api.NewClient(endpoint, token).RevokeSkill(ctx, skillID); err != nil {
		return fmt.Errorf("revoke skill: %w", err)
	}
	fmt.Printf("%s revoked.\n", skillID)
	fmt.Println("Note: for open-source shares this stops Telara serving the skill; it cannot recall copies already taken.")
	return nil
}

// confirm asks for an explicit y/N. preApproved short-circuits it for
// non-interactive use, which the caller only passes when the user supplied the
// flags that mean "I know".
func confirm(prompt string, preApproved bool) bool {
	if preApproved {
		return true
	}
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func pluraliseFindings(n int) string {
	switch n {
	case 0:
		return "clean."
	case 1:
		return "1 finding"
	default:
		return fmt.Sprintf("%d findings", n)
	}
}

func shortHash(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// shareGate decides whether a share may proceed given the scan result.
//
// A credential-grade finding REFUSES rather than prompts. Two reasons: a prompt
// normalises the decision, and --yes exists for automation — if critical
// findings were promptable, --yes would silently push credentials to a registry.
// Only --force, which a human must type per invocation, overrides it.
//
// Warn-level findings (internal hostnames, private IPs) do not block: they are
// real disclosure but often intentional in a team-scoped skill, and blocking
// them would train people to pass --force by reflex, which is what would make
// the critical gate useless.
func shareGate(findings []skillshare.Finding, force bool) error {
	if skillshare.HasCritical(findings) && !force {
		return fmt.Errorf("refusing to share: credential-grade findings above. Remove them, or re-run with --force to skip this local check (the server re-scans and may still refuse)")
	}
	return nil
}
