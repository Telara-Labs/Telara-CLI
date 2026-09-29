package cmd

// tap.go is the TAP client for Telara's authenticated users (TENG-2962): what
// the tenant has promoted, and pulling one onto this machine. The runner that
// executes a primitive is the open-source tap-runtime; this is the half that
// talks to the tenant's registry, so it lives in the telara CLI with the
// user's login rather than in any separate tool.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

var tapCmd = &cobra.Command{
	Use:   "tap",
	Short: "TAP primitives your organisation distributes",
	Long: `List and pull the TAP primitives your tenant has promoted.

A primitive is a program an agent runs through the TAP runner (tap-runtime).
Only versions an administrator promoted are listed or delivered.`,
}

var tapListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the primitives your tenant has promoted",
	Args:  cobra.NoArgs,
	RunE:  runTapList,
}

var tapPullCmd = &cobra.Command{
	Use:   "pull <publisher/name[@version]>",
	Short: "Pull a promoted primitive onto this machine",
	Long: `Pull a promoted primitive, at its latest promoted version unless @version
is given, and install it where your agent client loads skills. The agent runs
it with the TAP runner's tap_run tool.

The package's digest is verified before anything is written. --out writes the
verified package file instead of installing it.`,
	Args: cobra.ExactArgs(1),
	RunE: runTapPull,
}

func init() {
	tapListCmd.Flags().Bool("json", false, "Machine-readable output")
	tapPullCmd.Flags().String("client", "claude-code", "Which agent client to install into, or 'all' for every detected one")
	tapPullCmd.Flags().String("scope", "global", "Where to install: global | project")
	tapPullCmd.Flags().Bool("force", false, "Replace a same-name skill folder that is not an installed primitive")
	tapPullCmd.Flags().Bool("dry-run", false, "Fetch and verify, but write nothing")
	tapPullCmd.Flags().String("out", "", "Write the verified package (gzip tar) to this file instead of installing it")
	tapCmd.AddCommand(tapListCmd, tapPullCmd)
	rootCmd.AddCommand(tapCmd)
}

func tapClient() (*api.Client, error) {
	endpoint := config.ScanSubmitEndpoint()
	token, err := auth.LoadToken(endpoint)
	if err != nil {
		return nil, fmt.Errorf("not logged in — run: telara login --token <tlrc_...>")
	}
	return api.NewClient(endpoint, token), nil
}

func runTapList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	client, err := tapClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	list, err := client.ListPromotedPrimitives(ctx)
	if err != nil {
		return fmt.Errorf("list primitives: %w", err)
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"primitives": list})
	}
	if len(list) == 0 {
		fmt.Println("Your tenant has not promoted any primitives.")
		return nil
	}
	for _, p := range list {
		fmt.Println(p.Ref)
	}
	return nil
}

func runTapPull(cmd *cobra.Command, args []string) error {
	clientName, _ := cmd.Flags().GetString("client")
	scopeName, _ := cmd.Flags().GetString("scope")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	out, _ := cmd.Flags().GetString("out")

	scope, err := parseSkillScope(scopeName)
	if err != nil {
		return err
	}
	client, err := tapClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	if out != "" {
		p, err := fetchPrimitive(ctx, client, args[0])
		if err != nil {
			return err
		}
		if dryRun {
			fmt.Printf("[dry-run] would write %s (%d bytes, %s) to %s\n", p.Ref(), len(p.Package), p.ArtifactDigest, out)
			return nil
		}
		if err := os.WriteFile(out, p.Package, 0o600); err != nil {
			return err
		}
		fmt.Printf("%s  %s  %d bytes -> %s\n", p.Ref(), p.ArtifactDigest, len(p.Package), out)
		return nil
	}
	return installPrimitive(ctx, client, args[0], clientName, scope, force, dryRun)
}

// fetchPrimitive fetches a promoted version and verifies its digest.
func fetchPrimitive(ctx context.Context, client *api.Client, ref string) (skillshare.PrimitiveInstall, error) {
	pkg, err := client.GetPrimitivePackage(ctx, ref)
	if err != nil {
		return skillshare.PrimitiveInstall{}, fmt.Errorf("fetch primitive %q: %w", ref, err)
	}
	raw, err := base64.StdEncoding.DecodeString(pkg.PackageBase64)
	if err != nil {
		return skillshare.PrimitiveInstall{}, fmt.Errorf("fetch primitive %q: package is not base64: %w", ref, err)
	}
	p := skillshare.PrimitiveInstall{
		Publisher: pkg.Publisher, Name: pkg.Name, Version: pkg.Version,
		ArtifactDigest: pkg.ArtifactDigest, Package: raw,
	}
	return p, p.Verify()
}

// installPrimitive fetches a promoted primitive and writes it as a skill
// folder into each target client.
func installPrimitive(ctx context.Context, client *api.Client, ref, clientName string, scope agent.Scope, force, dryRun bool) error {
	in, err := fetchPrimitive(ctx, client, ref)
	if err != nil {
		return err
	}
	fmt.Printf("Primitive: %s\n", in.Ref())
	fmt.Printf("Digest:    %s\n", in.ArtifactDigest)

	targets, err := installTargets(clientName, scope)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no agent client with a skills directory was detected")
	}
	if dryRun {
		for _, t := range targets {
			fmt.Printf("\n[dry-run] would install to %s/%s/\n", t.dir, in.Name)
		}
		return nil
	}
	for _, t := range targets {
		res, werr := skillshare.InstallPrimitive(t.dir, in, force)
		if werr != nil {
			var notOurs *skillshare.ErrNotAPrimitiveFolder
			if errors.As(werr, &notOurs) {
				return fmt.Errorf("%w\n\nRe-run with --force to replace it", werr)
			}
			return werr
		}
		switch {
		case res.Unchanged:
			fmt.Printf("\nAlready installed %s -> %s\n", t.client, res.Path)
		case res.Overwrote:
			fmt.Printf("\nUpdated %s -> %s\n", t.client, res.Path)
		default:
			fmt.Printf("\nInstalled %s -> %s\n", t.client, res.Path)
		}
	}
	fmt.Println("\nRun it through the TAP runner's tap_run tool. If your client has no tap_run tool, connect the runner: tap-runtime install --client <client>")
	return nil
}
