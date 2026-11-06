package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// onboardingBinding is the credential a client is wired with, together with the
// identity of the configuration it belongs to.
//
// The ConfigID is what makes auto-selection safe (TENG-3017): the configuration
// is identified by the id the server returns, never by its display name. That
// name embeds the user's uuid rather than the config's (agent-service
// scopeBaseName), so matching the string "Personal" matches nothing and matching
// the uuid inside the name resolves to a different object entirely.
type onboardingBinding struct {
	RawKey string
	// KeyID is the server id of RawKey, cached with it so logout revokes this
	// exact key. Empty when the issuing route did not return one.
	KeyID      string
	MCPURL     string
	ConfigID   string
	ConfigName string
	// IsDefaultBinding means the server selected this config for a caller who
	// supplied no explicit config. The gateway checks user scope and owner but
	// does not expose an is-base assertion. Do not infer more from this flag.
	IsDefaultBinding bool
}

// onboardingCredential obtains the server-selected user binding for login and
// install bootstrap. Failure to issue it must not widen authority by silently
// selecting a tenant master or another configuration. Explicit configuration
// selection remains available through the regular install/config commands.
func onboardingCredential(ctx context.Context, client *api.Client, keyName string) (onboardingBinding, error) {
	base, err := client.IssueBaseKey(ctx, keyName)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusUnauthorized:
				return onboardingBinding{}, fmt.Errorf("login expired or is invalid; sign in again: %w", err)
			case http.StatusConflict:
				return onboardingBinding{}, fmt.Errorf("personal configuration is not ready; retry or select an authorized configuration: %w", err)
			case http.StatusNotFound, http.StatusNotImplemented:
				return onboardingBinding{}, fmt.Errorf("personal configuration activation is unavailable on this gateway; contact your administrator: %w", err)
			}
		}
		return onboardingBinding{}, fmt.Errorf("could not issue personal configuration key; check access and retry: %w", err)
	}
	return onboardingBinding{
		RawKey:           base.BaseKey,
		KeyID:            base.KeyID,
		MCPURL:           base.MCPURL,
		ConfigID:         base.MCPConfigID,
		ConfigName:       base.ConfigName,
		IsDefaultBinding: true,
	}, nil
}
