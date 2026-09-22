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

// onboardingBinding is the credential a client is wired with, together with the
// identity of the configuration it belongs to.
//
// The ConfigID is what makes auto-selection safe (TENG-3017): the configuration
// is identified by the id the server returns, never by its display name. That
// name embeds the user's uuid rather than the config's (agent-service
// scopeBaseName), so matching the string "Personal" matches nothing and matching
// the uuid inside the name resolves to a different object entirely.
type onboardingBinding struct {
	RawKey     string
	MCPURL     string
	ConfigID   string
	ConfigName string
	// IsDefaultBinding reports that the server chose this configuration for a
	// caller who named none — the binding a connection gets by default. It is
	// deliberately NOT called "is base": the base-key route is documented as
	// returning the caller's own base, but it returns whatever
	// ResolveMCPConfiguration selects and only checks that the result is
	// user-scoped. Resolution prefers any config with a user-scope default
	// deployment over the always-on base (agent-service
	// deployments.go ResolveConfiguration, step 1 before step 1b), so a user
	// who has one is bound to that instead, and the route reports it as their
	// base. Claiming "this is your Personal base" from this response would be
	// claiming something the server does not actually check (TENG-3023).
	//
	// False for the downgrade fallbacks, which must never be recorded as a
	// default: presenting the tenant master — the union of every policy in the
	// tenant — as a personal baseline is the exact inversion this avoids.
	IsDefaultBinding bool
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
func onboardingCredential(ctx context.Context, client *api.Client, keyName string) (onboardingBinding, error) {
	base, baseErr := client.IssueBaseKey(ctx, keyName)
	if baseErr == nil {
		return onboardingBinding{
			RawKey:           base.BaseKey,
			MCPURL:           base.MCPURL,
			ConfigID:         base.MCPConfigID,
			ConfigName:       base.ConfigName,
			IsDefaultBinding: true,
		}, nil
	}
	if !credentialFallbackPermitted(baseErr) {
		// Transient or ambiguous failure: fail with the base-key error instead
		// of silently binding the session to the tenant master.
		return onboardingBinding{}, fmt.Errorf("issue base configuration key: %w", baseErr)
	}

	master, masterErr := client.IssueMasterKey(ctx, keyName)
	if masterErr == nil {
		fmt.Fprintf(onboardingWarn,
			"\nWARNING: your personal base configuration is not available, so this session is\n"+
				"         now bound to the tenant MASTER configuration — the union of every\n"+
				"         policy in the tenant, not your personal least-privilege base.\n"+
				"         Base key error: %v\n\n", baseErr)
		return onboardingBinding{RawKey: master.MasterKey, MCPURL: master.MCPURL, ConfigName: "Master"}, nil
	}
	if !credentialFallbackPermitted(masterErr) {
		return onboardingBinding{}, fmt.Errorf("issue tenant master key (base key unavailable: %v): %w", baseErr, masterErr)
	}

	// Not every existing tenant has a usable master configuration yet. Fall
	// back to the user's first available deployed configuration, but surface a
	// concrete error if no path can supply a credential.
	resolved, resolveErr := client.ResolveConfigs(ctx)
	if resolveErr != nil {
		return onboardingBinding{}, fmt.Errorf("issue tenant master key (%v); resolve fallback configuration: %w", masterErr, resolveErr)
	}
	candidates := append(resolved.Managed, resolved.Available...)
	if len(candidates) == 0 {
		return onboardingBinding{}, fmt.Errorf("issue tenant master key (%v); no fallback MCP configuration is available", masterErr)
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
		return onboardingBinding{RawKey: key.RawKey, MCPURL: key.MCPURL, ConfigID: cfg.ID, ConfigName: cfg.Name}, nil
	}
	return onboardingBinding{}, fmt.Errorf("issue tenant master key (%v); no usable fallback configuration: %w", masterErr, lastErr)
}
