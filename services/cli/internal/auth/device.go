package auth

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/teleraai/telara-cli/services/cli/internal/api"
)

// DeviceFlowResult holds the response from initiating a device flow.
type DeviceFlowResult struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       int
	Interval        int
}

// StartDeviceFlow calls POST /v1/cli/auth/device/code and returns the device flow state.
func StartDeviceFlow(ctx context.Context, client *api.Client) (*DeviceFlowResult, error) {
	var resp api.DeviceFlowResponse
	req := api.DeviceFlowRequest{ClientName: "telara-cli"}
	if err := client.Post(ctx, "/v1/cli/auth/device/code", req, &resp); err != nil {
		return nil, fmt.Errorf("failed to initiate device flow: %w", err)
	}
	return &DeviceFlowResult{
		DeviceCode:      resp.DeviceCode,
		UserCode:        resp.UserCode,
		VerificationURI: resp.VerificationURI,
		ExpiresIn:       resp.ExpiresIn,
		Interval:        resp.Interval,
	}, nil
}

// PollForToken polls POST /v1/cli/auth/device/token until the user authorizes,
// the code expires, or ctx is cancelled. It respects the interval from the server.
// Returns the raw CLI token string on success.
func PollForToken(ctx context.Context, client *api.Client, deviceCode string, interval int) (string, error) {
	if interval <= 0 {
		interval = 5
	}
	pollInterval := time.Duration(interval) * time.Second

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		var resp api.PollDeviceResponse
		req := api.PollDeviceRequest{DeviceCode: deviceCode}
		if err := client.Post(ctx, "/v1/cli/auth/device/token", req, &resp); err != nil {
			return "", fmt.Errorf("failed to poll for token: %w", err)
		}

		switch resp.Status {
		case "complete":
			if resp.Token == "" {
				return "", fmt.Errorf("device flow complete but server returned empty token")
			}
			return resp.Token, nil
		case "pending":
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(pollInterval):
			}
		case "expired":
			return "", fmt.Errorf("device code expired — run 'telara login' again")
		case "denied":
			return "", fmt.Errorf("authorization was denied")
		default:
			return "", fmt.Errorf("unexpected device flow status: %q", resp.Status)
		}
	}
}
