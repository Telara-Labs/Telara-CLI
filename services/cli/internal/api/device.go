package api

// DeviceFlowRequest is the request body for POST /v1/cli/auth/device/code.
type DeviceFlowRequest struct {
	ClientName string `json:"client_name"`
}

// DeviceFlowResponse is the response body from initiating a device flow.
type DeviceFlowResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// PollDeviceRequest is the request body for POST /v1/cli/auth/device/token.
type PollDeviceRequest struct {
	DeviceCode string `json:"device_code"`
}

// PollDeviceResponse is the response body from polling for a device token.
type PollDeviceResponse struct {
	Status string `json:"status"`
	Token  string `json:"token"`
}
