// Package schedule installs a recurring, unattended `telara scan`.
//
// Discovery is only honest if it recurs. The estate model carries evidence
// freshness (current / aging / stale) and per-scope completeness, and those
// states are what stop the product claiming coverage it does not have. A
// one-shot manual scan decays to stale and the coverage rail stops telling the
// truth, so the schedule is part of the correctness story rather than a
// convenience.
//
// Design constraints:
//   - PER-USER, not system-wide. The scan reads the invoking user's own AI
//     client configuration; running it as root would read the wrong home
//     directory and could touch other users' files.
//   - VISIBLE AND REVOCABLE. The unit file is written in a standard location
//     under the user's own home so anyone can read exactly what runs, and
//     Uninstall removes it. An endpoint agent that hides itself is malware
//     behaviour, not security tooling.
//   - IDEMPOTENT. Installing twice replaces the unit rather than stacking jobs.
package schedule

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Label identifies the scheduled job on every platform.
const Label = "dev.telara.cli.scan"

// DefaultInterval is how often an unattended scan runs.
const DefaultInterval = "24h"

// Status describes whether a recurring scan is installed on this machine.
type Status struct {
	Installed bool
	Path      string
	Platform  string
	Detail    string
}

// Install registers a recurring scan for the CURRENT user. It is idempotent.
func Install() (Status, error) {
	exe, err := os.Executable()
	if err != nil {
		return Status{}, fmt.Errorf("resolve telara binary path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return Status{}, fmt.Errorf("resolve telara binary symlink: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(exe)
	case "linux":
		return installSystemdUser(exe)
	default:
		return Status{Platform: runtime.GOOS}, fmt.Errorf(
			"unattended scanning is not supported on %s yet; run `telara scan` manually or deploy it via your MDM",
			runtime.GOOS)
	}
}

// Uninstall removes the recurring scan. Absent is not an error.
func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		path, err := launchdPlistPath()
		if err != nil {
			return err
		}
		// Best effort: unload first so a running job stops, then remove.
		_ = exec.Command("launchctl", "unload", path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	case "linux":
		dir, err := systemdUserDir()
		if err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "disable", "--now", Label+".timer").Run()
		for _, name := range []string{Label + ".timer", Label + ".service"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil
	default:
		return nil
	}
}

// Current reports whether a recurring scan is installed.
func Current() Status {
	switch runtime.GOOS {
	case "darwin":
		path, err := launchdPlistPath()
		if err != nil {
			return Status{Platform: "darwin", Detail: err.Error()}
		}
		if _, err := os.Stat(path); err == nil {
			return Status{Installed: true, Path: path, Platform: "darwin"}
		}
		return Status{Platform: "darwin", Path: path}
	case "linux":
		dir, err := systemdUserDir()
		if err != nil {
			return Status{Platform: "linux", Detail: err.Error()}
		}
		path := filepath.Join(dir, Label+".timer")
		if _, err := os.Stat(path); err == nil {
			return Status{Installed: true, Path: path, Platform: "linux"}
		}
		return Status{Platform: "linux", Path: path}
	default:
		return Status{Platform: runtime.GOOS, Detail: "unsupported"}
	}
}

func launchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func systemdUserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// logPath keeps scan output where the user can read it. Discovery that runs
// unattended must leave an audit trail the employee can inspect.
func logPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".telara", "scan.log"), nil
}

func installLaunchd(exe string) (Status, error) {
	path, err := launchdPlistPath()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	logFile, err := logPath()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return Status{}, fmt.Errorf("create log dir: %w", err)
	}

	// StartInterval is seconds. RunAtLoad makes the first scan happen at login
	// rather than up to a day later, so a freshly enrolled machine appears in
	// inventory promptly instead of looking like uncovered coverage.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + exe + `</string>
		<string>scan</string>
	</array>
	<key>StartInterval</key>
	<integer>86400</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + logFile + `</string>
	<key>StandardErrorPath</key>
	<string>` + logFile + `</string>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>Nice</key>
	<integer>10</integer>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return Status{}, fmt.Errorf("write %s: %w", path, err)
	}
	// Reload so the change takes effect without requiring a logout.
	_ = exec.Command("launchctl", "unload", path).Run()
	if out, err := exec.Command("launchctl", "load", path).CombinedOutput(); err != nil {
		return Status{Path: path, Platform: "darwin"},
			fmt.Errorf("launchctl load failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return Status{Installed: true, Path: path, Platform: "darwin"}, nil
}

func installSystemdUser(exe string) (Status, error) {
	dir, err := systemdUserDir()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Status{}, fmt.Errorf("create systemd user dir: %w", err)
	}

	service := `[Unit]
Description=Telara AI estate discovery scan (read-only)
Documentation=https://telara.dev

[Service]
Type=oneshot
ExecStart=` + exe + ` scan
Nice=10
IOSchedulingClass=idle
`
	// Persistent=true catches up a scan missed while the machine was off,
	// otherwise a laptop that sleeps through its window silently goes stale.
	// RandomizedDelaySec spreads a fleet's submissions instead of stampeding.
	timer := `[Unit]
Description=Daily Telara AI estate discovery scan

[Timer]
OnBootSec=5min
OnUnitActiveSec=24h
Persistent=true
RandomizedDelaySec=30min

[Install]
WantedBy=timers.target
`
	if err := os.WriteFile(filepath.Join(dir, Label+".service"), []byte(service), 0o644); err != nil {
		return Status{}, fmt.Errorf("write service unit: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, Label+".timer"), []byte(timer), 0o644); err != nil {
		return Status{}, fmt.Errorf("write timer unit: %w", err)
	}

	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", Label+".timer").CombinedOutput(); err != nil {
		return Status{Path: filepath.Join(dir, Label+".timer"), Platform: "linux"},
			fmt.Errorf("systemctl enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return Status{Installed: true, Path: filepath.Join(dir, Label+".timer"), Platform: "linux"}, nil
}
