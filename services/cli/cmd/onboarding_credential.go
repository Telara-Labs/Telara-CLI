package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// onboardingWarn is where credential-downgrade warnings are written. Stderr in
// production; tests swap it to capture the output.
var onboardingWarn io.Writer = os.Stderr

// credentialFallbackPermitted reports whether err marks a credential route as
// definitively unavailable, which is the only condition under which the CLI may
// downgrade to a broader credential (TENG-2353):
//
//   - 404 Not Found        — the gateway is too old to serve the route
//   - 409 Conflict         — the server says no such configuration exists for
//     this user (IssueBaseKey returns this when resolution
//     does not yield the user's own base config)
//   - 501 Not Implemented  — the feature is explicitly not implemented
//
// Anything else — a network/transport failure, a timeout, a 5xx, an auth error,
// a malformed response — is a transient or ambiguous failure of a route that
// may well exist, and silently downgrading on it would bind the session to a
// far wider credential (the tenant master is the union of every policy in the
// tenant) than the user was meant to receive.
func credentialFallbackPermitted(err error) bool {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusNotFound, http.StatusConflict, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

// onboardingCredential obtains the user-bound credential used by both login
// auto-wiring and the explicit installer. Keeping this in one place prevents
// the two entry points from silently drifting back to different key lifecycles.
//
// The user's own base configuration is the default binding (TENG-2306): it is
// always on, least-privilege, and scoped to this user. The tenant master key
// and the first deployed configuration remain fallbacks for gateways and
// tenants that cannot serve a base key — but only when the preceding error is
// a definitive "not available" (see credentialFallbackPermitted), and never
// silently: every downgrade prints an unmissable warning naming the broader
// credential the session is now bound to and the concrete error that caused it.
func onboardingCredential(ctx context.Context, client *api.Client, keyName string) (rawKey, mcpURL, configName string, err error) {
	base, baseErr := client.IssueBaseKey(ctx, keyName)
	if baseErr == nil {
		return base.BaseKey, base.MCPURL, base.ConfigName, nil
	}
	if !credentialFallbackPermitted(baseErr) {
		// Transient or ambiguous failure: fail with the base-key error instead
		// of silently binding the session to the tenant master.
		return "", "", "", fmt.Errorf("issue base configuration key: %w", baseErr)
	}

	master, masterErr := client.IssueMasterKey(ctx, keyName)
	if masterErr == nil {
		fmt.Fprintf(onboardingWarn,
			"\nWARNING: your personal base configuration is not available, so this session is\n"+
				"         now bound to the tenant MASTER configuration — the union of every\n"+
				"         policy in the tenant, not your personal least-privilege base.\n"+
				"         Base key error: %v\n\n", baseErr)
		return master.MasterKey, master.MCPURL, "Master", nil
	}
	if !credentialFallbackPermitted(masterErr) {
		return "", "", "", fmt.Errorf("issue tenant master key (base key unavailable: %v): %w", baseErr, masterErr)
	}

	// Not every existing tenant has a usable master configuration yet. Fall
	// back to the user's first available deployed configuration, but surface a
	// concrete error if no path can supply a credential.
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
		fmt.Fprintf(onboardingWarn,
			"\nWARNING: neither your personal base configuration nor the tenant master key is\n"+
				"         available; this session is now bound to the deployed configuration\n"+
				"         %q rather than your personal least-privilege base.\n"+
				"         Base key error:   %v\n"+
				"         Master key error: %v\n\n", cfg.Name, baseErr, masterErr)
		return key.RawKey, key.MCPURL, cfg.Name, nil
	}
	return "", "", "", fmt.Errorf("issue tenant master key (%v); no usable fallback configuration: %w", masterErr, lastErr)
}
