package cmd

// tap_offer.go is the offer telara scan makes once, on a terminal, to set up
// TAP and look for repeated work. It explains what TAP does, links to it, and
// on yes installs the runner if it is missing, connects it to the agents
// here (tap setup) and runs discover. The answer is kept in config.json so
// the offer is not repeated.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/config"
	"gitlab.com/telara-labs/telara-cli/services/cli/internal/display"
)

const tapProjectURL = "https://github.com/Telara-Labs/TAP-Runtime"

// tapOfferText says what TAP does and why, before asking.
func tapOfferText() string {
	return "\n" +
		" " + display.Accent("▸") + " " + display.Strong("Find work your agents repeat") + "\n" +
		"   TAP reads the session history your AI agents keep on this machine, finds\n" +
		"   the tasks you ask for again and again, and saves each one as a small\n" +
		"   program your agents run instead of redoing the steps. It runs locally:\n" +
		"   nothing leaves this machine unless you publish a primitive yourself.\n" +
		"   Learn more: " + tapProjectURL + "\n\n" +
		"   Set up TAP and look now? [y/N] "
}

// shouldOfferTAP is whether telara scan asks: only an interactive terminal,
// never with --json or --dry-run, and only until it has been answered.
func shouldOfferTAP(interactive, asJSON, dryRun bool, p *config.Prefs) bool {
	return interactive && !asJSON && !dryRun && p != nil && p.TapOffer == ""
}

// tapSteps are the commands a yes runs. Tests replace run.
type tapSteps struct {
	findRunner func() string
	lookPath   func(string) (string, error)
	run        func(name string, args ...string) error
}

func realTapSteps(in io.Reader, out, errOut io.Writer) tapSteps {
	return tapSteps{
		findRunner: findRunner,
		lookPath:   exec.LookPath,
		run: func(name string, args ...string) error {
			c := exec.Command(name, args...)
			c.Stdin, c.Stdout, c.Stderr = in, out, errOut
			return c.Run()
		},
	}
}

var errNoNPM = errors.New("npm is not installed")

// setUpTAPAndDiscover installs the runner when it is missing, connects it to
// this machine's agents and runs discover.
func setUpTAPAndDiscover(s tapSteps, out io.Writer) error {
	runner := s.findRunner()
	if runner == "" {
		npm, err := s.lookPath("npm")
		if err != nil {
			fmt.Fprintf(out, "\n   TAP installs with npm, which is not on this machine.\n   Install Node.js, then run: npm install -g @telaralabs/tap\n   More: %s\n", tapProjectURL)
			return errNoNPM
		}
		fmt.Fprintln(out, "\n   Installing TAP: npm install -g @telaralabs/tap")
		if err := s.run(npm, "install", "-g", "@telaralabs/tap"); err != nil {
			return fmt.Errorf("npm install -g @telaralabs/tap: %w", err)
		}
		if runner = s.findRunner(); runner == "" {
			return errors.New("npm installed TAP, but tap is not on PATH; open a new terminal and run: telara tap discover")
		}
	}
	if err := s.run(runner, "setup"); err != nil {
		return fmt.Errorf("tap setup: %w", err)
	}
	return s.run(runner, "discover")
}

// offerTAP asks once and acts on the answer. It never fails the scan.
func offerTAP(in io.Reader, out io.Writer, s tapSteps) {
	fmt.Fprint(out, tapOfferText())
	answer, _ := bufio.NewReader(in).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	yes := answer == "y" || answer == "yes"
	prefs.TapOffer = "declined"
	if yes {
		prefs.TapOffer = "accepted"
	}
	if err := config.Save(prefs); err != nil {
		fmt.Fprintf(os.Stderr, "   (could not save your answer: %v)\n", err)
	}
	if !yes {
		fmt.Fprintln(out, "   Skipped. Run it any time with: telara tap discover")
		return
	}
	if err := setUpTAPAndDiscover(s, out); err != nil && !errors.Is(err, errNoNPM) {
		fmt.Fprintf(os.Stderr, "   TAP: %v\n", err)
	}
}
