package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// The author path's subcommands reach the discover module with their own
// flags, rather than being refused as arguments `telara tap discover` does
// not take.
func TestTapDiscoverAuthorSubcommandsReachDiscover(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"tap", "discover", "brief"}, "give --out and exactly one of --task or --candidate"},
		{[]string{"tap", "discover", "validate", "--freeze"}, "--cases is required"},
		{[]string{"tap", "discover", "save"}, "give one package directory"},
	} {
		var out, errb bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&errb)
		rootCmd.SetArgs(c.args)
		err := rootCmd.Execute()
		if err == nil {
			t.Errorf("%v: a missing required argument must fail", c.args)
		}
		if !strings.Contains(errb.String(), c.want) {
			t.Errorf("%v: stderr %q lacks the discover module's own message %q", c.args, errb.String(), c.want)
		}
	}
}
