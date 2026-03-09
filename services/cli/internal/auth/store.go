package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
	"gitlab.com/teleraai/telera-cli/services/cli/internal/config"
)

const keyringService = "telara-cli"

// ErrNoToken is returned when no stored token is found for the given API URL.
var ErrNoToken = errors.New("no token stored")

// credentialFile holds the JSON structure written to disk for the file fallback.
type credentialFile struct {
	Token string `json:"token"`
}

// sanitizeHost converts an API URL to a safe filename component.
// e.g. "https://api.telara.ai" -> "api.telara.ai"
func sanitizeHost(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("invalid API URL %q: %w", apiURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("cannot determine hostname from API URL %q", apiURL)
	}
	// Replace any characters that are unsafe in filenames
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(host)
	return safe, nil
}

// SaveToken stores token for the given apiURL.
// It tries the OS keychain first; on failure it falls back to a file in CredentialsDir.
func SaveToken(apiURL, token string) error {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return err
	}

	// Try OS keychain first
	if err := keyring.Set(keyringService, host, token); err == nil {
		return nil
	}

	// Fall back to file-based storage
	return saveTokenToFile(host, token)
}

// LoadToken retrieves the stored token for the given apiURL.
// Returns ErrNoToken if no credential is found.
func LoadToken(apiURL string) (string, error) {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return "", err
	}

	// Try OS keychain first
	token, err := keyring.Get(keyringService, host)
	if err == nil && token != "" {
		return token, nil
	}

	// Fall back to file-based storage
	return loadTokenFromFile(host)
}

// DeleteToken removes the stored token for the given apiURL from both the
// OS keychain (if present) and the file fallback.
func DeleteToken(apiURL string) error {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return err
	}

	// Attempt keychain deletion (ignore error — may not be stored there)
	_ = keyring.Delete(keyringService, host)

	// Attempt file deletion (ignore ErrNotExist)
	return deleteTokenFile(host)
}

// --- file-based fallback ---

func credFilePath(host string) (string, error) {
	dir, err := config.CredentialsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, host+".json"), nil
}

func saveTokenToFile(host, token string) error {
	path, err := credFilePath(host)
	if err != nil {
		return err
	}

	data, err := json.Marshal(credentialFile{Token: token})
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write credential file: %w", err)
	}
	return nil
}

func loadTokenFromFile(host string) (string, error) {
	path, err := credFilePath(host)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoToken
		}
		return "", fmt.Errorf("read credential file: %w", err)
	}

	var cred credentialFile
	if err := json.Unmarshal(data, &cred); err != nil {
		return "", fmt.Errorf("parse credential file: %w", err)
	}

	if cred.Token == "" {
		return "", ErrNoToken
	}
	return cred.Token, nil
}

func deleteTokenFile(host string) error {
	path, err := credFilePath(host)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credential file: %w", err)
	}
	return nil
}
