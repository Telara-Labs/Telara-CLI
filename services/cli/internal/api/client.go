package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Client is an HTTP client for the Telara API.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// NewClient creates a new API client for the given base URL and bearer token.
//
// TLS behaviour (checked in order):
//  1. TELARA_INSECURE=true  — skip verification entirely (escape hatch, not recommended)
//  2. TELARA_CA_CERT_PATH   — load a custom CA bundle (local dev with self-signed cert)
//  3. default               — system CA pool (works for Let's Encrypt / public CAs)
func NewClient(baseURL, token string) *Client {
	var transport http.RoundTripper = http.DefaultTransport

	if os.Getenv("TELARA_INSECURE") == "true" {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	} else if caPath := os.Getenv("TELARA_CA_CERT_PATH"); caPath != "" {
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: TELARA_CA_CERT_PATH: cannot read %s: %v\n", caPath, err)
			os.Exit(1)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			fmt.Fprintf(os.Stderr, "error: TELARA_CA_CERT_PATH: no valid certs found in %s\n", caPath)
			os.Exit(1)
		}
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}
	}

	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
}

// APIError represents a non-2xx response from the Telara API.
type APIError struct {
	StatusCode int
	Message    string
	Body       string // raw response body
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
}

// Post performs a POST request to the given path with the given body, decoding
// the response into out (if non-nil). It does not require an auth token, making
// it suitable for public endpoints such as device flow polling.
func (c *Client) Post(ctx context.Context, path string, body, out interface{}) error {
	return c.do(ctx, "POST", path, body, out)
}

// do performs an HTTP request, injects the auth header, and decodes the JSON
// response into out (if non-nil). Returns *APIError for non-2xx responses.
func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reqBody *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBytes, _ := io.ReadAll(resp.Body)
		rawBody := string(rawBytes)

		// Try to extract a human-readable message from JSON.
		msg := extractJSONMessage(rawBytes)
		if msg == "" {
			// Fall back to raw body, truncated to 500 chars.
			msg = rawBody
			if len(msg) > 500 {
				msg = msg[:500]
			}
			if msg == "" {
				msg = resp.Status
			}
		}

		return &APIError{StatusCode: resp.StatusCode, Message: msg, Body: rawBody}
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// extractJSONMessage tries to pull a "message" or "error" string from a JSON body.
// Returns empty string if the body is not valid JSON or neither field is present.
func extractJSONMessage(data []byte) string {
	var errResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &errResp); err != nil {
		return ""
	}
	if errResp.Message != "" {
		return errResp.Message
	}
	return errResp.Error
}
