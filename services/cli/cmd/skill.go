package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
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

var skillInstallCmd = &cobra.Command{
	Use:   "install <skill-name|skill-id>",
	Short: "Install a shared skill onto this machine",
	Long: `Download an approved shared skill and write it where your agent will load it.

The registry's content hash is VERIFIED before anything is written, and the body
is re-scanned locally. Only clients with a real skills directory can be written
to; for the others, connect them to Telara's MCP server and use telara_skill_load.`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillInstall,
}

var skillRevokeCmd = &cobra.Command{
	Use:   "revoke <skill-id>",
	Short: "Withdraw a shared skill",
	Args:  cobra.ExactArgs(1),
	RunE:  runSkillRevoke,
}

func init() {
	skillShareCmd.Flags().String("scope", "", "Who receives it: team | enterprise (required)")
	skillShareCmd.Flags().String("audience", "", "Narrow the audience: team:<id> | user:<id>. Omitted means you alone.")
	skillShareCmd.Flags().Bool("yes", false, "Skip the interactive prompt (still refuses on critical findings unless --force)")
	skillShareCmd.Flags().Bool("force", false, "Skip the LOCAL scan check. The server re-scans and may still refuse")
	skillShareCmd.Flags().Bool("dry-run", false, "Run the scan and print what would be sent, without sending it")

	skillInstallCmd.Flags().String("client", "claude-code", "Which agent client to install into, or 'all' for every detected one")
	skillInstallCmd.Flags().String("scope", "global", "Where to install: global | project")
	skillInstallCmd.Flags().Bool("force", false, "Overwrite a locally modified SKILL.md")
	skillInstallCmd.Flags().Bool("dry-run", false, "Fetch and verify, but write nothing")

	skillCmd.AddCommand(skillListCmd, skillShareCmd, skillInstallCmd, skillRevokeCmd)
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
		// A PENDING skill is shared but not loadable by anyone. Without this
		// the line was identical to a live one, so an author had no way to see
		// that their enterprise share was still sitting in a review queue.
		state := ""
		switch {
		case s.Revoked:
			state = "  [revoked]"
		case s.ApprovalState == "pending":
			state = "  [awaiting approval — not loadable yet]"
		}
		// A narrower audience than the whole tenant is worth showing: it is the
		// difference between "everyone has this" and "one team does".
		audience := ""
		if s.TargetScopeType != "" && s.TargetScopeType != "tenant" {
			audience = "  ->" + s.TargetScopeType
		}
		fmt.Printf("  %-28s v%-3d %-11s %s%s%s\n", s.Name, s.Version, s.Scope, s.SkillID, audience, state)
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
		return fmt.Errorf("--scope is required (team | enterprise)")
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
		return fmt.Errorf(
			"open-source sharing is not available.\n\n" +
				"It never published anywhere outside your tenant, while asking you to " +
				"consent to public disclosure. Publish to your team, or request " +
				"tenant-wide promotion from an admin.")
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
	if resp.ApprovalState == "pending" {
		fmt.Println("NOT loadable yet: reaching the whole tenant needs approval from a tenant admin.")
	}
	// The SERVER's verdict, which is the enforcing one. Printed even on
	// success: warn-level findings do not block, and until now the author
	// never learned about them at all.
	printRiskVerdict(resp.Risk)
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

// runSkillInstall fetches one approved skill and writes it to disk.
func runSkillInstall(cmd *cobra.Command, args []string) error {
	ref := args[0]
	clientName, _ := cmd.Flags().GetString("client")
	scopeName, _ := cmd.Flags().GetString("scope")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	scope, err := parseSkillScope(scopeName)
	if err != nil {
		return err
	}

	endpoint := config.ScanSubmitEndpoint()
	token, err := auth.LoadToken(endpoint)
	if err != nil {
		return fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	detail, err := api.NewClient(endpoint, token).GetSharedSkill(ctx, ref)
	if err != nil {
		return fmt.Errorf("fetch skill %q: %w", ref, err)
	}

	fmt.Printf("Skill:   %s (v%d, %s)\n", detail.Name, detail.Version, detail.Scope)
	fmt.Printf("Hash:    %s\n", detail.ContentHash)
	fmt.Printf("Shared by: %s\n", detail.SharedBy)
	if detail.StalePolicyVersion {
		fmt.Println("NOTE:    this skill's security assessment predates the current scanner rules.")
	}
	// Assets are counted at share time but never uploaded (see share.go), so
	// there is nothing to install. Said plainly rather than leaving someone with
	// a skill that references files they do not have.
	if detail.AssetCount > 0 {
		fmt.Printf("NOTE:    the author bundled %d asset file(s). The registry does not store them, "+
			"so they are NOT installed.\n", detail.AssetCount)
	}
	printRiskVerdict(detail.Risk)

	targets, err := installTargets(clientName, scope)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no agent client with a skills directory was detected")
	}

	if dryRun {
		for _, t := range targets {
			fmt.Printf("\n[dry-run] would install to %s/%s/SKILL.md\n", t.dir, detail.Name)
		}
		return nil
	}

	for _, t := range targets {
		res, werr := skillshare.WriteSkill(t.dir, detail.Name, detail.Body, detail.ContentHash, force)
		if werr != nil {
			var modErr *skillshare.ErrLocalModification
			if errors.As(werr, &modErr) {
				return fmt.Errorf("%w\n\nRe-run with --force to overwrite it", werr)
			}
			return werr
		}
		verb := "Installed"
		if res.Overwrote {
			verb = "Updated"
		}
		fmt.Printf("\n%s %s -> %s\n", verb, t.client, res.Path)
	}
	return nil
}

type installTarget struct {
	client string
	dir    string
}

// installTargets resolves which clients to write into.
//
// A detected client with no skills directory is NAMED, with the alternative
// that actually works for it. Silently skipping would leave someone believing
// Cursor had the skill; failing outright would be unhelpful when claude-code is
// also present and did get it.
func installTargets(clientName string, scope agent.Scope) ([]installTarget, error) {
	var out []installTarget
	var withoutSkills []string

	for _, w := range agent.AllWriters() {
		if clientName != "all" && w.Name() != clientName {
			continue
		}
		if clientName == "all" && !w.Detect() {
			continue
		}
		sw, ok := w.(agent.SkillsWriter)
		if !ok {
			withoutSkills = append(withoutSkills, w.Name())
			continue
		}
		dir, err := sw.SkillsDir(scope)
		if err != nil {
			return nil, err
		}
		out = append(out, installTarget{client: w.Name(), dir: dir})
	}

	for _, name := range withoutSkills {
		fmt.Fprintf(os.Stderr,
			"%s has no skills directory — connect it to Telara's MCP server and use "+
				"telara_skill_load instead of installing to disk.\n", name)
	}
	if len(out) == 0 && clientName != "all" && len(withoutSkills) == 0 {
		return nil, fmt.Errorf("unknown client %q", clientName)
	}
	return out, nil
}

// parseSkillScope is deliberately NOT cmd/install.go's parseInstallScope.
//
// That one accepts global|managed, because an MCP config can be deployed by an
// enterprise administrator. Skills accept global|project instead: a skill is
// workspace-shaped (a project can reasonably carry its own runbook) and there
// is no managed skills directory to write to. Same word, different axis.
func parseSkillScope(name string) (agent.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "global", "":
		return agent.ScopeGlobal, nil
	case "project":
		return agent.ScopeProject, nil
	default:
		return 0, fmt.Errorf("scope must be 'global' or 'project', got %q", name)
	}
}

// printRiskVerdict shows what the SERVER recorded.
//
// Printed on share and on install alike. The CLI used to discard this entirely,
// so an author never saw the warn-level findings that did not block — the ones
// worth acting on before somebody else loads the skill.
func printRiskVerdict(v *api.RiskVerdict) {
	if v == nil {
		return
	}
	fmt.Printf("Scan:    score %d of threshold %d", v.Score, v.Threshold)
	if v.PolicyVersion != "" {
		fmt.Printf(" (%s)", v.PolicyVersion)
	}
	fmt.Println()
	for _, fr := range v.FiredRules {
		marker := " "
		if fr.Severity == "critical" {
			marker = "!"
		}
		fmt.Printf("  %s %s %s line %d (%s, %d pts) — %s\n",
			marker, fr.RuleID, fr.Name, fr.Line, fr.Severity, fr.Points, fr.Explanation)
	}
}
