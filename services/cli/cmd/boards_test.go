package cmd

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

var sgrCodes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = sgrCodes.ReplaceAllString(l, "")
	}
	return out
}

// boxesAligned checks that every row between a box's top and bottom ends a
// box edge in the same column as the top's corner.
func boxesAligned(t *testing.T, lines []string) {
	t.Helper()
	for i, top := range lines {
		for j, r := range []rune(top) {
			if r != '╮' && r != '┐' {
				continue
			}
			for k := i + 1; k < len(lines); k++ {
				row := []rune(lines[k])
				if j >= len(row) {
					t.Fatalf("row %d is shorter than the box above it:\n%s", k, strings.Join(lines, "\n"))
				}
				if row[j] == '╯' || row[j] == '┘' {
					break
				}
				if row[j] != '│' {
					t.Fatalf("row %d column %d is %q, not the box edge:\n%s", k, j, row[j], strings.Join(lines, "\n"))
				}
			}
		}
	}
}

func TestLoginBoardBoxesLineUpInEveryState(t *testing.T) {
	b := &loginBoard{url: "https://app.telara.dev/device", code: "WXYZ-82K4", expires: time.Now().Add(time.Minute)}
	for state := 0; state < 3; state++ {
		b.state = state
		for f := -1; f < 25; f++ {
			boxesAligned(t, plainLines(b.render(f)))
		}
	}
	b.state = 0
	got := strings.Join(plainLines(b.render(3)), "\n")
	for _, want := range []string{"https://app.telara.dev/device", "Enter code: WXYZ-82K4", "expires in 0:"} {
		if !strings.Contains(got, want) {
			t.Errorf("waiting board lacks %q:\n%s", want, got)
		}
	}
}

func TestInstallBoardShowsEachOutcome(t *testing.T) {
	b := &installBoard{names: []string{"Claude Code", "Cursor", "Codex"}, scope: "global", results: make([]*installResult, 3)}
	if got := strings.Join(plainLines(b.render(2)), "\n"); strings.Count(got, "connecting…") != 3 {
		t.Fatalf("before any result every client should be connecting:\n%s", got)
	}
	b.done(0, installResult{client: "Claude Code", status: "CONNECTED", detail: "engineering-default"})
	b.done(1, installResult{client: "Cursor", status: "FAILED", detail: "permission denied"})
	got := strings.Join(plainLines(b.render(display.Final)), "\n")
	for _, want := range []string{"TELARA ━┳", "✓ connected · engineering-default", "✗ permission denied", "· not reached", "Restart your AI clients"} {
		if !strings.Contains(got, want) {
			t.Errorf("final install board lacks %q:\n%s", want, got)
		}
	}
	var nilBoard *installBoard
	nilBoard.done(0, installResult{})
	nilBoard.stop(nil)
}

func TestNotifyCallsEveryCallbackWithTheResult(t *testing.T) {
	var seen []int
	r := notify([]func(int, installResult){func(i int, _ installResult) { seen = append(seen, i) }}, 4, installResult{client: "x"})
	if r.client != "x" || len(seen) != 1 || seen[0] != 4 {
		t.Fatalf("notify returned %+v, saw %v", r, seen)
	}
}

func TestShouldOfferTAP(t *testing.T) {
	asked := &config.Prefs{TapOffer: "declined"}
	fresh := &config.Prefs{}
	for _, c := range []struct {
		interactive, asJSON, dryRun bool
		prefs                       *config.Prefs
		want                        bool
	}{
		{true, false, false, fresh, true},
		{false, false, false, fresh, false},
		{true, true, false, fresh, false},
		{true, false, true, fresh, false},
		{true, false, false, asked, false},
		{true, false, false, &config.Prefs{TapOffer: "accepted"}, false},
	} {
		if got := shouldOfferTAP(c.interactive, c.asJSON, c.dryRun, c.prefs); got != c.want {
			t.Errorf("shouldOfferTAP(%v,%v,%v,%q) = %v, want %v", c.interactive, c.asJSON, c.dryRun, c.prefs.TapOffer, got, c.want)
		}
	}
	text := sgrCodes.ReplaceAllString(tapOfferText(), "")
	for _, want := range []string{"https://github.com/Telara-Labs/TAP-Runtime", "nothing leaves", "[y/N]"} {
		if !strings.Contains(text, want) {
			t.Errorf("offer text lacks %q", want)
		}
	}
}

// A yes installs the runner only when it is missing, then connects it and
// runs discover, in that order.
func TestSetUpTAPAndDiscover(t *testing.T) {
	var out strings.Builder
	var ran []string
	installed := false
	s := tapSteps{
		findRunner: func() string {
			if installed {
				return "/usr/local/bin/tap"
			}
			return ""
		},
		lookPath: func(string) (string, error) { return "/usr/local/bin/npm", nil },
		run: func(name string, args ...string) error {
			ran = append(ran, name+" "+strings.Join(args, " "))
			if strings.HasSuffix(name, "npm") {
				installed = true
			}
			return nil
		},
	}
	if err := setUpTAPAndDiscover(s, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/npm install -g @telaralabs/tap", "/usr/local/bin/tap setup", "/usr/local/bin/tap discover"}
	if strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Fatalf("ran %q, want %q", ran, want)
	}

	ran = nil
	if err := setUpTAPAndDiscover(s, &out); err != nil || len(ran) != 2 {
		t.Fatalf("with tap installed: ran %q, err %v", ran, err)
	}

	installed = false
	s.lookPath = func(string) (string, error) { return "", errors.New("no npm") }
	ran = nil
	out.Reset()
	if err := setUpTAPAndDiscover(s, &out); !errors.Is(err, errNoNPM) || len(ran) != 0 || !strings.Contains(out.String(), "npm install -g @telaralabs/tap") {
		t.Fatalf("without npm: err %v, ran %q, said %q", err, ran, out.String())
	}
}
