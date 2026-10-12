package cmd

// tap_signing_keys.go fetches the public keys that verify package attestations
// and keeps them on disk with the time they were fetched.
//
// A cached key is trusted for keyCacheTTL. A signature made by a key version
// the cache does not hold (a rotation) forces a refetch. If the refetch fails
// the cache is used only when it already holds that version: an offline pull of
// a package signed by a known key still verifies, but an unknown key never does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/skillshare"
)

const (
	keyCacheTTL  = 24 * time.Hour
	keyCacheFile = "tap-signing-keys.json"
)

type signingKeyCache struct {
	Endpoint  string                  `json:"endpoint"`
	FetchedAt time.Time               `json:"fetched_at"`
	Keys      []skillshare.SigningKey `json:"keys"`
}

func keyCachePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, keyCacheFile), nil
}

func readKeyCache(path, endpoint string) (signingKeyCache, bool) {
	var c signingKeyCache
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Endpoint != endpoint || len(c.Keys) == 0 {
		return signingKeyCache{}, false
	}
	return c, true
}

func writeKeyCache(path string, c signingKeyCache) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hasKeyVersion(keys []skillshare.SigningKey, version string) bool {
	for _, k := range keys {
		if k.Version == version {
			return true
		}
	}
	return false
}

// signingKeysFor returns the published keys, from the cache when it is fresh
// and holds keyVersion, otherwise fetched and cached.
func signingKeysFor(ctx context.Context, path, endpoint, keyVersion string, now time.Time,
	fetch func(context.Context) ([]skillshare.SigningKey, error)) ([]skillshare.SigningKey, error) {
	cached, haveCache := readKeyCache(path, endpoint)
	if haveCache && now.Sub(cached.FetchedAt) < keyCacheTTL && hasKeyVersion(cached.Keys, keyVersion) {
		return cached.Keys, nil
	}
	keys, err := fetch(ctx)
	if err != nil {
		if haveCache && hasKeyVersion(cached.Keys, keyVersion) {
			return cached.Keys, nil
		}
		return nil, fmt.Errorf("fetch signing keys: %w", err)
	}
	if len(keys) == 0 {
		return nil, errors.New("fetch signing keys: the registry published none")
	}
	_ = writeKeyCache(path, signingKeyCache{Endpoint: endpoint, FetchedAt: now.UTC(), Keys: keys})
	return keys, nil
}

func fetchSigningKeysFrom(client *api.Client) func(context.Context) ([]skillshare.SigningKey, error) {
	return func(ctx context.Context) ([]skillshare.SigningKey, error) {
		keys, err := client.GetPrimitiveSigningKeys(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]skillshare.SigningKey, 0, len(keys))
		for _, k := range keys {
			out = append(out, skillshare.SigningKey{KeyID: k.KeyID, Version: k.Version, Alg: k.Alg, PublicKeyB64: k.PublicKeyB64})
		}
		return out, nil
	}
}
