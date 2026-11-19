package clientidentity_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/agent"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/clientidentity"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/discovery"
)

func TestRegistrySnapshotMatchesWritersAndDiscovery(t *testing.T) {
	data, err := os.ReadFile("registry.snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != clientidentity.SourceSHA256 {
		t.Fatal("registry snapshot digest differs from generated constants")
	}
	var snapshot struct {
		SchemaVersion int `json:"schema_version"`
		Identities    []struct {
			CLIWriter string `json:"cli_writer"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatal("unsupported registry schema")
	}
	var expected, writers []string
	for _, entry := range snapshot.Identities {
		if entry.CLIWriter != "" {
			expected = append(expected, entry.CLIWriter)
		}
	}
	for _, writer := range agent.AllWriters() {
		writers = append(writers, writer.Name())
	}
	discovered := []string{discovery.ClientClaudeCode, discovery.ClientCursor, discovery.ClientWindsurf, discovery.ClientVSCode, discovery.ClientCodex, discovery.ClientGemini, discovery.ClientAmazonQ}
	sort.Strings(expected)
	sort.Strings(writers)
	sort.Strings(discovered)
	if !reflect.DeepEqual(expected, writers) || !reflect.DeepEqual(expected, discovered) {
		t.Fatalf("registry %v, writers %v, discovery %v", expected, writers, discovered)
	}
}
