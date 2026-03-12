package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
	"gitlab.com/teleraai/telara-cli/services/cli/internal/config"
	"golang.org/x/crypto/pbkdf2"
)

const (
	snapshotKeyringService = "telara-cli"
	snapshotKeyringAccount = "snapshot-key"
	snapshotKeyFileName    = "snapshot.key"
	pbkdf2Iterations       = 100_000
	aesKeyLen              = 32 // AES-256
)

// pbkdf2Salt is a fixed salt used to derive the AES key from the keychain secret.
// Changing this would invalidate all existing snapshots.
var pbkdf2Salt = []byte("telara-cli-snapshot-v1")

// getOrCreateSnapshotKey retrieves or generates the 32-byte secret used for
// snapshot encryption. It tries the OS keychain first, falling back to a file.
func getOrCreateSnapshotKey() ([]byte, error) {
	// Try keychain
	secret, err := keyring.Get(snapshotKeyringService, snapshotKeyringAccount)
	if err == nil && len(secret) > 0 {
		return deriveKey([]byte(secret)), nil
	}

	// Try file fallback
	keyPath, err := snapshotKeyFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == aesKeyLen {
		// File secret exists — derive key from it
		return deriveKey(data), nil
	}

	// Generate new secret
	newSecret := make([]byte, aesKeyLen)
	if _, err := rand.Read(newSecret); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}

	// Try to save in keychain
	if err := keyring.Set(snapshotKeyringService, snapshotKeyringAccount, string(newSecret)); err == nil {
		return deriveKey(newSecret), nil
	}

	// Fall back to file
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(keyPath, newSecret, 0600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}

	return deriveKey(newSecret), nil
}

func snapshotKeyFilePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, snapshotKeyFileName), nil
}

func deriveKey(secret []byte) []byte {
	return pbkdf2.Key(secret, pbkdf2Salt, pbkdf2Iterations, aesKeyLen, sha256.New)
}

// encryptAESGCM encrypts plaintext using AES-256-GCM with a random nonce prepended.
func encryptAESGCM(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// nonce is prepended to the ciphertext
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptAESGCM decrypts ciphertext produced by encryptAESGCM.
func decryptAESGCM(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}
