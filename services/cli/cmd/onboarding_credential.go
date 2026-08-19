package cmd

import (
	"context"
	"fmt"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// onboardingCredential obtains the user-bound credential used by both login
// auto-wiring and the explicit installer. Keeping this in one place prevents
// the two entry points from silently drifting back to different key lifecycles.
func onboardingCredential(ctx context.Context, client *api.Client, keyName string) (rawKey, mcpURL, configName string, err error) {
	// The user's own base configuration is the default binding (TENG-2306): it is
	// always on, least-privilege, and scoped to this user. Only fall back to the
	// tenant master when the gateway is too old to serve the base route — the
	// master's effective policy is the union of every policy in the tenant, so it
	// is a credential of last resort, not a default.
	if base, baseErr := client.IssueBaseKey(ctx, keyName); baseErr == nil {
		return base.BaseKey, base.MCPURL, base.ConfigName, nil
	}

	master, masterErr := client.IssueMasterKey(ctx, keyName)
	if masterErr == nil {
		return master.MasterKey, master.MCPURL, "Master", nil
	}

	// Not every existing tenant has a usable master configuration yet. Fall
	// back to the user's first available deployed configuration, but surface a
	// concrete error if neither path can supply a credential.
	resolved, resolveErr := client.ResolveConfigs(ctx)
	if resolveErr != nil {
		return "", "", "", fmt.Errorf("issue tenant master key (%v); resolve fallback configuration: %w", masterErr, resolveErr)
	}
	candidates := append(resolved.Managed, resolved.Available...)
	if len(candidates) == 0 {
		return "", "", "", fmt.Errorf("issue tenant master key (%v); no fallback MCP configuration is available", masterErr)
	}
	var lastErr error
	for _, cfg := range candidates {
		deps, err := client.ListDeployments(ctx, cfg.ID)
		if err != nil {
			lastErr = fmt.Errorf("load deployments for %s: %w", cfg.Name, err)
			continue
		}
		if len(deps.Deployments) == 0 {
			lastErr = fmt.Errorf("%s has no deployment", cfg.Name)
			continue
		}
		dep := &deps.Deployments[0]
		for i := range deps.Deployments {
			if deps.Deployments[i].ScopeType == "tenant" {
				dep = &deps.Deployments[i]
				break
			}
		}

		key, err := client.GenerateKey(ctx, cfg.ID, api.GenerateKeyRequest{
			Name:      keyName,
			ScopeType: dep.ScopeType,
			ScopeID:   dep.ScopeID,
		})
		if err != nil {
			lastErr = fmt.Errorf("issue fallback key for %s: %w", cfg.Name, err)
			continue
		}
		if key.RawKey == "" {
			lastErr = fmt.Errorf("fallback key response for %s did not include a key", cfg.Name)
			continue
		}
		return key.RawKey, key.MCPURL, cfg.Name, nil
	}
	return "", "", "", fmt.Errorf("issue tenant master key (%v); no usable fallback configuration: %w", masterErr, lastErr)
}
