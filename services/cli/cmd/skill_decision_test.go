package cmd

import "testing"

// The approval gate publishes instruction text that every colleague's agent
// will read and obey, so the ways a decision can be made must be enumerable.
//
// The refused combination is --yes with no --version: it would apply the
// decision to whatever is pending when the script runs, which is the race the
// version field exists to prevent. Every other combination is allowed, because
// each still puts the exact version in front of a human before recording.
func TestSkillDecisionFlagsRefuseOnlyTheBlindCombination(t *testing.T) {
	cases := []struct {
		name      string
		version   int
		assumeYes bool
		wantErr   bool
	}{
		{"scripted with an explicit version", 3, true, false},
		{"interactive, version resolved and confirmed", 0, false, false},
		{"interactive with an explicit version", 3, false, false},
		{"scripted with NO version — decides blind", 0, true, true},
		{"scripted with a nonsense version", -1, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSkillDecisionFlags(tc.version, tc.assumeYes)
			if tc.wantErr && err == nil {
				t.Fatal("this combination decides on bytes nobody named and must be refused")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// approve and reject must be separate commands. A single command with a flag
// can be reached by OMITTING the flag, and on a gate that publishes to the
// whole tenant the default must not be reachable by accident.
func TestApproveAndRejectAreSeparateCommands(t *testing.T) {
	names := map[string]bool{}
	for _, c := range skillCmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"pending", "approve", "reject"} {
		if !names[want] {
			t.Errorf("telara skill %s is missing — the approval gate has no operator without it", want)
		}
	}
	for _, c := range skillCmd.Commands() {
		if c.Name() != "approve" && c.Name() != "reject" {
			continue
		}
		// Neither may carry a flag that flips it into the other verb.
		for _, forbidden := range []string{"approve", "reject", "decision"} {
			if c.Flags().Lookup(forbidden) != nil {
				t.Errorf("%s must not take a --%s flag; the verb is the command", c.Name(), forbidden)
			}
		}
		if c.Flags().Lookup("version") == nil {
			t.Errorf("%s must accept --version: approval attaches to content, not to a name", c.Name())
		}
	}
}
