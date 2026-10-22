package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/auth"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Revoke your CLI token, clean up MCP configs, and remove local credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			// Not logged in — clean up any local file silently, treat as success
			_ = auth.DeleteToken(prefs.APIURL)
			display.PrintSuccess("Logged out")
			return nil
		}

		client := api.NewClient(prefs.APIURL, token)

		// Capture identity before revoking so the snapshot is keyed to this user.
		var userID, tenantID string
		if whoami, err := client.ValidateToken(context.Background()); err == nil {
			userID = whoami.UserID
			tenantID = whoami.TenantID
		}

		// Revoke the cached MCP key while the token can still authorise it. A
		// key that is deleted locally but never revoked stays usable.
		if err := revokeCachedMCPKey(context.Background(), client); err != nil {
			return err
		}

		if err := client.RevokeToken(context.Background()); err != nil {
			if !tokenRejected(err) {
				// Keep the local credential so the revoke can be retried.
				return fmt.Errorf("the token is still valid on the server, the revoke failed and nothing was removed locally. Run telara logout again once the server is reachable. %w", err)
			}
		}

		// Snapshot + clean up MCP configs before deleting the token.
		cleanupMCPConfigs(userID, tenantID)

		if err := auth.DeleteToken(prefs.APIURL); err != nil {
			return fmt.Errorf("failed to remove local token: %w", err)
		}

		display.PrintSuccess("Logged out")
		return nil
	},
}

// tokenRejected reports that the server refused the CLI token itself, which
// means it is not live and there is nothing left to revoke.
func tokenRejected(err error) bool {
	var apiErr *api.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

// revokeCachedMCPKey revokes the MCP key `telara install` cached beside the
// login token. The cache holds only the raw key, so the key is found by the
// prefix the server lists for each configuration the user can reach.
func revokeCachedMCPKey(ctx context.Context, client *api.Client) error {
	raw, err := auth.LoadMCPKey(prefs.APIURL)
	if errors.Is(err, auth.ErrNoMCPKey) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the cached MCP key: %w", err)
	}
	configs, err := client.ListConfigs(ctx)
	if err != nil {
		if tokenRejected(err) {
			return nil
		}
		return fmt.Errorf("the cached MCP key is still valid on the server, listing configurations to revoke it failed and nothing was removed locally. %w", err)
	}
	for _, cfg := range configs.Configs {
		keys, err := client.ListKeys(ctx, cfg.ID)
		if err != nil {
			return fmt.Errorf("the cached MCP key may still be valid on the server, listing keys for %s failed and nothing was removed locally. %w", cfg.Name, err)
		}
		for _, k := range keys.Keys {
			if k.Revoked || len(k.Prefix) < 8 || !strings.HasPrefix(raw, k.Prefix) {
				continue
			}
			if err := client.RevokeKey(ctx, k.ID, cfg.ID); err != nil {
				return fmt.Errorf("the cached MCP key %s is still valid on the server, the revoke failed and nothing was removed locally. %w", k.Prefix, err)
			}
			return nil
		}
	}
	fmt.Fprintln(os.Stderr, "Warning: the cached MCP key matches no key on your configurations, so there was nothing to revoke. It is removed locally.")
	return nil
}

// scopeString converts an agent.Scope to a string for snapshot storage.
func scopeString(s agent.Scope) string {
	switch s {
	case agent.ScopeGlobal:
		return "global"
	case agent.ScopeProject:
		return "project"
	case agent.ScopeManaged:
		return "managed"
	default:
		return "unknown"
	}
}

// cleanupMCPConfigs collects all "telara" MCP entries, saves a snapshot, then removes them.
func cleanupMCPConfigs(userID, tenantID string) {
	var entries []agent.SnapshotEntry

	// Determine if managed-layer removal is appropriate.
	// Only remove managed entries when the active context is tenant-scoped.
	removeManagedLayer := false
	store, err := newContextStore()
	if err == nil {
		ctxs, err := store.List()
		if err == nil {
			// Check active context (or any context — if all are tenant-scoped, remove managed).
			for _, c := range ctxs {
				if prefs.ActiveContext != "" && c.Name == prefs.ActiveContext && c.ScopeType == "tenant" {
					removeManagedLayer = true
					break
				}
			}
			// If no active context is set but there are contexts, check first one.
			if prefs.ActiveContext == "" && len(ctxs) > 0 && ctxs[0].ScopeType == "tenant" {
				removeManagedLayer = true
			}
		}
	}

	// Save original working directory for restoration after project-scope operations.
	origDir, _ := os.Getwd()

	// Collect global and managed entries from all detected writers.
	for _, w := range agent.DetectedWriters() {
		// Global scope
		if globalEntries, err := w.Read(agent.ScopeGlobal); err == nil {
			if entry, ok := globalEntries["telara"]; ok {
				entries = append(entries, agent.SnapshotEntry{
					Tool:       w.Name(),
					Scope:      "global",
					ServerName: "telara",
					Entry:      entry,
				})
			}
		}

		// Managed scope (only collect if we intend to remove it)
		if removeManagedLayer {
			if managedEntries, err := w.Read(agent.ScopeManaged); err == nil {
				if entry, ok := managedEntries["telara"]; ok {
					entries = append(entries, agent.SnapshotEntry{
						Tool:       w.Name(),
						Scope:      "managed",
						ServerName: "telara",
						Entry:      entry,
					})
				}
			}
		}
	}

	// Collect project-scope entries from all registered projects.
	projects, err := agent.ListProjects()
	if err == nil {
		for _, proj := range projects {
			for _, toolName := range proj.Tools {
				w := agent.WriterByName(toolName)
				if w == nil {
					continue
				}
				// Change to project directory so the writer resolves the correct config path.
				if err := os.Chdir(proj.Path); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: cannot access project %s: %v\n", proj.Path, err)
					continue
				}
				if projectEntries, err := w.Read(agent.ScopeProject); err == nil {
					if entry, ok := projectEntries["telara"]; ok {
						entries = append(entries, agent.SnapshotEntry{
							Tool:       toolName,
							Scope:      "project",
							ServerName: "telara",
							Entry:      entry,
							ProjectDir: proj.Path,
						})
					}
				}
			}
		}
	}

	// Restore original working directory.
	if origDir != "" {
		_ = os.Chdir(origDir)
	}

	// Save snapshot before removing anything.
	if len(entries) > 0 {
		if err := agent.SaveSnapshot(entries, userID, tenantID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save MCP config snapshot: %v\n", err)
		}
	}

	// Remove entries.
	removedCount := 0

	// Remove global entries.
	for _, w := range agent.DetectedWriters() {
		if err := w.Remove(agent.ScopeGlobal, "telara"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove global %s config: %v\n", w.Name(), err)
		} else {
			removedCount++
		}
		if pw, ok := w.(agent.PermissionWriter); ok {
			if err := pw.RemovePermissions(agent.ScopeGlobal, "telara"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to remove %s auto-approve rule: %v\n", w.Name(), err)
			}
		}
	}

	// Remove project entries.
	if projects != nil {
		for _, proj := range projects {
			for _, toolName := range proj.Tools {
				w := agent.WriterByName(toolName)
				if w == nil {
					continue
				}
				if err := os.Chdir(proj.Path); err != nil {
					continue
				}
				if err := w.Remove(agent.ScopeProject, "telara"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to remove project config for %s in %s: %v\n", toolName, proj.Path, err)
				} else {
					removedCount++
				}
				if pw, ok := w.(agent.PermissionWriter); ok {
					if err := pw.RemovePermissions(agent.ScopeProject, "telara"); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to remove %s auto-approve rule in %s: %v\n", toolName, proj.Path, err)
					}
				}
			}
			_ = agent.UnregisterProject(proj.Path)
		}
		if origDir != "" {
			_ = os.Chdir(origDir)
		}
	}

	// Remove managed entries (only if tenant-scoped).
	if removeManagedLayer {
		for _, w := range agent.DetectedWriters() {
			if err := w.Remove(agent.ScopeManaged, "telara"); err != nil {
				if os.IsPermission(err) {
					fmt.Fprintf(os.Stderr, "Warning: managed config for %s requires elevated permissions to remove\n", w.Name())
				} else {
					fmt.Fprintf(os.Stderr, "Warning: failed to remove managed %s config: %v\n", w.Name(), err)
				}
			} else {
				removedCount++
			}
			if pw, ok := w.(agent.PermissionWriter); ok {
				if err := pw.RemovePermissions(agent.ScopeManaged, "telara"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to remove %s managed auto-approve rule: %v\n", w.Name(), err)
				}
			}
		}
	}

	if len(entries) > 0 {
		display.PrintInfo("MCP configs saved. They will be restored automatically when you log back in.")
	}
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}
