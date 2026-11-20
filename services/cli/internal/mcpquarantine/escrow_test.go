package mcpquarantine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

func TestStripEntryLeavesNoCredential(t *testing.T) {
	raw := json.RawMessage(`{"command":"npx","args":["-y","pkg","--api-key","sk-live-1","TOKEN=abc","--verbose"],"env":{"API_TOKEN":"sk-2","REGION":"us"},"url":"https://u:p@host.example/mcp?key=sk-3","headers":{"Authorization":"Bearer x"}}`)
	out, keys, err := StripEntry(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-live-1", "abc", "sk-2", "sk-3", "Bearer x", "u:p"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("escrow copy still carries %q: %s", secret, out)
		}
	}
	for _, kept := range []string{`"npx"`, `"pkg"`, `"--verbose"`, `host.example/mcp`} {
		if !strings.Contains(string(out), kept) {
			t.Errorf("escrow copy lost %s: %s", kept, out)
		}
	}
	sort.Strings(keys)
	want := []string{"args[3]", "args[4]", "env.API_TOKEN", "env.REGION", "headers.Authorization", "url.query"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("stripped keys = %v, want %v", keys, want)
	}
}

func TestRestoreFromEscrowPutsTheEntryBackWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	var target *discovery.MCPConfigFile
	for _, f := range discovery.QuarantinableMCPFiles(home) {
		if f.ClientID == "cursor" {
			f := f
			target = &f
		}
	}
	if target == nil {
		t.Skip("no cursor user-level MCP file in the catalog")
	}
	act, err := RestoreFromEscrow(home, target.ClientID, target.ItemsKey, "gh", json.RawMessage(`{"url":"https://x.example/mcp","headers":{"Authorization":""}}`))
	if err != nil || act.Kind != "reinstated_from_escrow" {
		t.Fatalf("restore: %+v %v", act, err)
	}
	data, _ := os.ReadFile(target.Path)
	if !strings.Contains(string(data), `"gh"`) {
		t.Fatalf("entry not written: %s", data)
	}
	act, err = RestoreFromEscrow(home, target.ClientID, target.ItemsKey, "gh", json.RawMessage(`{"url":"https://other"}`))
	if err != nil || act.Kind != "conflict" {
		t.Fatalf("second restore over an existing entry: %+v %v", act, err)
	}
	if HasRecord(home, target.ClientID, "gh") {
		t.Error("no local record was written")
	}
	_ = filepath.Join
}
