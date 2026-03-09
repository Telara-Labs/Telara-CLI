package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ConfigDir returns the platform-appropriate config directory for telara.
// macOS:   ~/Library/Application Support/telara
// Linux:   $XDG_CONFIG_HOME/telara or ~/.config/telara
// Windows: %APPDATA%\telara
// The directory is created with 0700 permissions if it does not exist.
func ConfigDir() (string, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		base = filepath.Join(home, "Library", "Application Support", "telara")

	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return "", fmt.Errorf("APPDATA environment variable not set")
		}
		base = filepath.Join(appdata, "telara")

	default:
		// Linux and others — respect XDG_CONFIG_HOME
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg != "" {
			base = filepath.Join(xdg, "telara")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("cannot determine home directory: %w", err)
			}
			base = filepath.Join(home, ".config", "telara")
		}
	}

	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("failed to create config directory %q: %w", base, err)
	}
	return base, nil
}

// CredentialsDir returns the directory used to store credential files.
// Falls back to ~/.telara/credentials on all platforms.
// The directory is created with 0700 permissions if it does not exist.
func CredentialsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".telara", "credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create credentials directory %q: %w", dir, err)
	}
	return dir, nil
}

// CacheDir returns the platform-appropriate cache directory for telara.
// macOS:   ~/Library/Caches/telara
// Linux:   $XDG_CACHE_HOME/telara or ~/.cache/telara
// Windows: %LOCALAPPDATA%\telara\cache
// The directory is created with 0700 permissions if it does not exist.
func CacheDir() (string, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		base = filepath.Join(home, "Library", "Caches", "telara")

	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			return "", fmt.Errorf("LOCALAPPDATA environment variable not set")
		}
		base = filepath.Join(localAppData, "telara", "cache")

	default:
		xdg := os.Getenv("XDG_CACHE_HOME")
		if xdg != "" {
			base = filepath.Join(xdg, "telara")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("cannot determine home directory: %w", err)
			}
			base = filepath.Join(home, ".cache", "telara")
		}
	}

	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("failed to create cache directory %q: %w", base, err)
	}
	return base, nil
}
