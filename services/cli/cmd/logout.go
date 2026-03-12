package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/agent"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/auth"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Revoke your CLI token, clean up MCP configs, and remove local credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := auth.LoadToken(prefs.APIURL)
		if err != nil {
			// Not logged in — clean up any local file silently, treat as success
			_ = auth.DeleteToken(prefs.APIURL)
			fmt.Fprintln(os.Stdout, "Logged out")
			return nil
		}

		client := api.NewClient(prefs.APIURL, token)

		// Capture identity before revoking so the snapshot is keyed to this user.
		var userID, tenantID string
		if whoami, err := client.ValidateToken(context.Background()); err == nil {
			userID = whoami.UserID
			tenantID = whoami.TenantID
		}

		if err := client.RevokeToken(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to revoke token server-side: %v\n", err)
			// Still delete locally
		}

		// Snapshot + clean up MCP configs before deleting the token.
		cleanupMCPConfigs(userID, tenantID)

		if err := auth.DeleteToken(prefs.APIURL); err != nil {
			return fmt.Errorf("failed to remove local token: %w", err)
		}

		fmt.Fprintln(os.Stdout, "Logged out")
		return nil
	},
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
		}
	}

	if len(entries) > 0 {
		fmt.Fprintln(os.Stdout, "MCP configs saved. They will be restored automatically when you log back in.")
	}
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}
