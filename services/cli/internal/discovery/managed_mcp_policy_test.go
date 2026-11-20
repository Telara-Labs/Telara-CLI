package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func withManagedSettings(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "managed-settings.json")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := managedSettingsPaths
	managedSettingsPaths = []string{p}
	t.Cleanup(func() { managedSettingsPaths = old })
}

func TestManagedPolicyDeniesByURLOrCommandNeverByName(t *testing.T) {
	withManagedSettings(t, `{"deniedMcpServers":[{"serverUrl":"https://*.evil.example/*"},{"serverCommand":["npx","-y","bad-mcp"]},{"serverName":"github"}]}`)
	p := LoadManagedMCPPolicy()
	cases := []struct {
		name, url, cmd string
		args           []string
		want           bool
	}{
		{"x", "https://api.evil.example/mcp", "", nil, true},
		{"x", "https://good.example/mcp", "", nil, false},
		{"x", "", "npx", []string{"-y", "bad-mcp"}, true},
		{"x", "", "npx", []string{"-y", "bad-mcp", "--extra"}, false},
		{"github", "https://api.github.example/mcp", "", nil, false},
	}
	for _, c := range cases {
		if got := p.Denies(c.name, c.url, c.cmd, c.args); got != c.want {
			t.Errorf("Denies(%q,%q,%q,%v) = %v, want %v", c.name, c.url, c.cmd, c.args, got, c.want)
		}
	}
}

func TestManagedAllowlistOnlyDeniesWhatItDoesNotAdmit(t *testing.T) {
	withManagedSettings(t, `{"allowManagedMcpServersOnly":true,"allowedMcpServers":[{"serverUrl":"https://mcp.telara.example/*"},{"serverName":"named"}]}`)
	p := LoadManagedMCPPolicy()
	if p.Denies("x", "https://mcp.telara.example/v1", "", nil) {
		t.Error("an allowed URL was denied")
	}
	if !p.Denies("x", "https://other.example/mcp", "", nil) {
		t.Error("a server outside the allowlist was not denied")
	}
	if p.Denies("named", "https://other.example/mcp", "", nil) {
		t.Error("a name-allowed server cannot be shown as enforced")
	}
	withManagedSettings(t, `{"allowedMcpServers":[{"serverUrl":"https://mcp.telara.example/*"}]}`)
	if LoadManagedMCPPolicy().Denies("x", "https://other.example/mcp", "", nil) {
		t.Error("without allowManagedMcpServersOnly, user settings can widen the list: not enforced")
	}
	withManagedSettings(t, `{"allowManagedMcpServersOnly":true,"allowedMcpServers":[]}`)
	if !LoadManagedMCPPolicy().Denies("x", "https://anything.example/mcp", "", nil) {
		t.Error("an empty allowlist with allow-only is a lockdown")
	}
}

func TestNoOrUnreadableManagedSettingsDeniesNothing(t *testing.T) {
	withManagedSettings(t, "")
	if LoadManagedMCPPolicy().Denies("x", "https://a.example/mcp", "", nil) {
		t.Error("no file must deny nothing")
	}
	withManagedSettings(t, "{not json")
	if LoadManagedMCPPolicy().Denies("x", "https://a.example/mcp", "", nil) {
		t.Error("an unreadable file must deny nothing")
	}
}

func TestManagedPolicyPresentOnlyWithAReadableFile(t *testing.T) {
	withManagedSettings(t, "")
	if LoadManagedMCPPolicy().Present() {
		t.Error("no file is not a managed device")
	}
	withManagedSettings(t, `{}`)
	if !LoadManagedMCPPolicy().Present() {
		t.Error("a readable managed settings file makes a managed device")
	}
	withManagedSettings(t, "{oops")
	if LoadManagedMCPPolicy().Present() {
		t.Error("an unreadable file is not a managed device")
	}
}
