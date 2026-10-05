package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// telara tap discover hands every argument, subcommands and flags alike, to
// the runner's own discover.
func TestTapDiscoverRunsTheRunnersDiscover(t *testing.T) {
	record := fakeRunner(t)
	for _, args := range [][]string{
		{"--days", "7", "--client", "codex"},
		{"brief", "--out", "b.md", "--task", "close a ticket"},
		{"--help"},
	} {
		rootCmd.SetArgs(append([]string{"tap", "discover"}, args...))
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range strings.Split(strings.TrimSpace(string(raw)), "----") {
		if c = strings.TrimSpace(c); c != "" {
			got = append(got, strings.ReplaceAll(c, "\n", " "))
		}
	}
	want := []string{
		"discover --days 7 --client codex",
		"discover brief --out b.md --task close a ticket",
		"discover --help",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("runner calls = %q, want %q", got, want)
	}
}

func TestTapDiscoverSaysHowToInstallTheRunner(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rootCmd.SetArgs([]string{"tap", "discover"})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "npm install -g @telaralabs/tap") {
		t.Fatalf("err = %v", err)
	}
}
