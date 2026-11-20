package schedule

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ReceiverLabel identifies the device receiver's service (TENG-3921). It runs
// `telara receiver run` for the current user and is restarted if it exits, so
// the held connection comes back after a crash. It exits cleanly, and is not
// restarted, once Telara stops accepting its key (logout). Like the
// scan, it is per user, visible in a standard location and removed by
// UninstallReceiver.
const ReceiverLabel = "dev.telara.cli.receiver"

// InstallReceiver registers the receiver service for the current user.
func InstallReceiver() (Status, error) {
	exe, err := os.Executable()
	if err != nil {
		return Status{}, fmt.Errorf("resolve telara binary path: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return Status{}, fmt.Errorf("resolve telara binary symlink: %w", err)
	}
	logFile, err := logPath()
	if err != nil {
		return Status{}, err
	}
	logFile = filepath.Join(filepath.Dir(logFile), "receiver.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return Status{}, fmt.Errorf("create log dir: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return Status{}, err
		}
		path := filepath.Join(home, "Library", "LaunchAgents", ReceiverLabel+".plist")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Status{}, err
		}
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + ReceiverLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + exe + `</string>
		<string>receiver</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>StandardOutPath</key>
	<string>` + logFile + `</string>
	<key>StandardErrorPath</key>
	<string>` + logFile + `</string>
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
`
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
			return Status{}, fmt.Errorf("write %s: %w", path, err)
		}
		_ = exec.Command("launchctl", "unload", path).Run()
		if out, err := exec.Command("launchctl", "load", path).CombinedOutput(); err != nil {
			return Status{Path: path, Platform: "darwin"}, fmt.Errorf("launchctl load failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return Status{Installed: true, Path: path, Platform: "darwin"}, nil
	case "linux":
		dir, err := systemdUserDir()
		if err != nil {
			return Status{}, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Status{}, err
		}
		unit := `[Unit]
Description=Telara device receiver (applies MCP server and skill removals)
Documentation=https://telara.dev

[Service]
ExecStart=` + exe + ` receiver run
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`
		path := filepath.Join(dir, ReceiverLabel+".service")
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			return Status{}, fmt.Errorf("write service unit: %w", err)
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", ReceiverLabel+".service").CombinedOutput(); err != nil {
			return Status{Path: path, Platform: "linux"}, fmt.Errorf("systemctl enable failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return Status{Installed: true, Path: path, Platform: "linux"}, nil
	}
	return Status{Platform: runtime.GOOS}, fmt.Errorf("the receiver service is not supported on %s yet; run `telara receiver run` under your own service manager", runtime.GOOS)
}

// UninstallReceiver removes the receiver service. Absent is not an error.
func UninstallReceiver() error {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := filepath.Join(home, "Library", "LaunchAgents", ReceiverLabel+".plist")
		_ = exec.Command("launchctl", "unload", path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	case "linux":
		dir, err := systemdUserDir()
		if err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "disable", "--now", ReceiverLabel+".service").Run()
		if err := os.Remove(filepath.Join(dir, ReceiverLabel+".service")); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return nil
}
