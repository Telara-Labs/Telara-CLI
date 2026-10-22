package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
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
			// The Gateway reports expired and denied as HTTP 400 with {status}.
			if terminal := terminalDeviceStatus(err); terminal != nil {
				return "", terminal
			}
			if !transientPollError(err) {
				return "", fmt.Errorf("failed to poll for token: %w", err)
			}
			// A 429, a 5xx or a dropped connection during the wait is not the
			// end of the login. The caller's context bounds the retrying.
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("failed to poll for token: %w", err)
			case <-time.After(pollInterval):
			}
			continue
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
			return "", errDeviceExpired
		case "denied":
			return "", errDeviceDenied
		default:
			return "", fmt.Errorf("unexpected device flow status: %q", resp.Status)
		}
	}
}

var (
	errDeviceExpired = fmt.Errorf("device code expired — run 'telara login' again")
	errDeviceDenied  = fmt.Errorf("authorization was denied")
)

// terminalDeviceStatus maps an HTTP 400 carrying {"status":"expired"|"denied"}
// to the CLI's own error, or returns nil.
func terminalDeviceStatus(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return nil
	}
	var body struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &body) != nil {
		return nil
	}
	switch body.Status {
	case "expired":
		return errDeviceExpired
	case "denied":
		return errDeviceDenied
	}
	return nil
}

// transientPollError reports a failure worth retrying: rate limiting, a server
// or gateway error, or a transport error. Any other API refusal is final.
func transientPollError(err error) bool {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}
