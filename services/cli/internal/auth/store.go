package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
)

// ErrNoToken is returned when no stored token is found for the given API URL.
var ErrNoToken = errors.New("no token stored")

// ErrNoMCPKey is returned when no MCP API key is stored for the given API URL.
var ErrNoMCPKey = errors.New("no MCP key stored")

// credentialFile holds the JSON structure written to disk for the file fallback.
//
// Token and MCPKey share one 0600 file per host and are written
// read-modify-write, so saving either keeps the other.
type credentialFile struct {
	Token string `json:"token"`
	// MCPKey is the raw MCP API key, kept so `telara mcp stdio` can authenticate
	// without the key reaching an environment variable or a command line where
	// ps would show it (TENG-3019). Only the stdio shim needs it: an http client
	// config carries its own copy.
	MCPKey string `json:"mcp_key,omitempty"`
	// MCPKeyID is the server id of MCPKey. Key prefixes are shared by every key
	// of a tenant, so logout revokes by this id; an entry cached before the id
	// was stored has none.
	MCPKeyID string `json:"mcp_key_id,omitempty"`
}

// sanitizeHost converts an API URL to a safe filename component that names its
// whole origin. A token saved for https://api.telara.dev is never loaded for
// http://api.telara.dev or another port, since the bearer would then travel over
// a different, possibly plaintext, endpoint.
//
// The default https origin keeps the bare hostname, so credentials saved by
// earlier versions still load.
// e.g. "https://api.telara.dev" -> "api.telara.dev"
//
//	"http://api.telara.dev"        -> "http_api.telara.dev"
//	"https://api.telara.dev:8443"  -> "https_api.telara.dev_8443"
func sanitizeHost(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("invalid API URL %q: %w", apiURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("cannot determine hostname from API URL %q", apiURL)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	port := u.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	name := host
	if scheme != "https" || port != "" {
		name = scheme + "_" + host
		if port != "" {
			name += "_" + port
		}
	}
	// Replace any characters that are unsafe in filenames
	return strings.NewReplacer(":", "_", "/", "_").Replace(name), nil
}

// SaveToken stores token for the given apiURL in the credentials file.
func SaveToken(apiURL, token string) error {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return err
	}
	return saveTokenToFile(host, token)
}

// LoadToken retrieves the stored token for the given apiURL.
// Returns ErrNoToken if no credential is found.
func LoadToken(apiURL string) (string, error) {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return "", err
	}
	return loadTokenFromFile(host)
}

// DeleteToken removes the stored token for the given apiURL.
func DeleteToken(apiURL string) error {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return err
	}
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
	return updateCredentialFile(host, func(cred *credentialFile) {
		cred.Token = token
	})
}

// updateCredentialFile applies mutate to the host's stored credentials and
// writes them back.
//
// Read-modify-write rather than marshalling a fresh struct: the file holds both
// the login token and the MCP key, and a blind overwrite of one would silently
// delete the other.
func updateCredentialFile(host string, mutate func(*credentialFile)) error {
	path, err := credFilePath(host)
	if err != nil {
		return err
	}

	cred, err := readCredentialFile(path)
	if err != nil {
		return err
	}
	mutate(cred)

	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}

	return writeFileOwnerOnly(path, data)
}

// writeFileOwnerOnly replaces path with data through a temp file and a rename.
// os.WriteFile applies its mode only when it creates the file, so an existing
// group or world readable credential file would keep its mode, and it truncates
// in place, so an interrupted write would leave partial JSON. The temp file is
// created 0600 and renamed over the target, which leaves either the old or the
// new complete file and always an owner-only mode.
func writeFileOwnerOnly(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cred-*")
	if err != nil {
		return fmt.Errorf("write credential file: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write credential file: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write credential file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("write credential file: %w", err)
	}
	return nil
}

// readCredentialFile returns the host's stored credentials, or an empty set when
// nothing is stored yet.
func readCredentialFile(path string) (*credentialFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &credentialFile{}, nil
		}
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	var cred credentialFile
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse credential file: %w", err)
	}
	return &cred, nil
}

// SaveMCPKey stores the raw MCP API key for the given apiURL, beside the login
// token for the same host. The key replaces any earlier one, so an id stored
// for the earlier key is dropped.
func SaveMCPKey(apiURL, key string) error {
	return SaveMCPKeyWithID(apiURL, key, "")
}

// SaveMCPKeyWithID stores the raw MCP API key together with its server id, so
// logout can revoke exactly this key.
func SaveMCPKeyWithID(apiURL, key, keyID string) error {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return err
	}
	return updateCredentialFile(host, func(cred *credentialFile) {
		cred.MCPKey = key
		cred.MCPKeyID = keyID
	})
}

// LoadMCPKeyID returns the server id stored with the MCP key, or "" when the
// key was cached without one.
func LoadMCPKeyID(apiURL string) (string, error) {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return "", err
	}
	path, err := credFilePath(host)
	if err != nil {
		return "", err
	}
	cred, err := readCredentialFile(path)
	if err != nil {
		return "", err
	}
	return cred.MCPKeyID, nil
}

// LoadMCPKey retrieves the stored MCP API key for the given apiURL.
// Returns ErrNoMCPKey when none is stored.
func LoadMCPKey(apiURL string) (string, error) {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return "", err
	}
	path, err := credFilePath(host)
	if err != nil {
		return "", err
	}
	cred, err := readCredentialFile(path)
	if err != nil {
		return "", err
	}
	if cred.MCPKey == "" {
		return "", ErrNoMCPKey
	}
	return cred.MCPKey, nil
}

// DeleteMCPKey removes the stored MCP API key, leaving the login token in place.
func DeleteMCPKey(apiURL string) error {
	host, err := sanitizeHost(apiURL)
	if err != nil {
		return err
	}
	return updateCredentialFile(host, func(cred *credentialFile) {
		cred.MCPKey = ""
		cred.MCPKeyID = ""
	})
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
