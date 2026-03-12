package display

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/term"
)

// ActionHint describes a single numbered action in the hint menu.
type ActionHint struct {
	Label       string   // short label shown next to the number, e.g. "Sign in again"
	Command     []string // argv to exec, e.g. []string{"telara", "login"}
	Description string   // command shown to the user, e.g. "telara login"
}

// ShowHints prints a numbered hint menu to stderr and reads ONE raw keypress.
// If the user presses a valid digit within 30 s the corresponding command is
// exec'd and this process exits with its exit code.
// Returns silently on timeout, invalid key, non-TTY stderr, or empty hints.
func ShowHints(title string, hints []ActionHint) {
	if !StderrIsTTY() {
		return
	}
	if len(hints) == 0 {
		return
	}

	fmt.Fprintln(os.Stderr)
	if title != "" {
		fmt.Fprintln(os.Stderr, "  "+ColorBold.Sprint(title))
	} else {
		fmt.Fprintln(os.Stderr, "  "+ColorDim.Sprint("Suggested actions:"))
	}
	fmt.Fprintln(os.Stderr)

	for i, h := range hints {
		num := ColorCmd.Sprintf("[%d]", i+1)
		fmt.Fprintf(os.Stderr, "  %s  %-22s %s\n", num, h.Description, ColorDim.Sprint(h.Label))
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s", ColorDim.Sprint("Press a number to run, or any other key to skip: "))

	// Put stdin into raw mode to capture a single keypress without Enter.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr)
		return
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintln(os.Stderr)
		return
	}

	restored := false
	restoreOnce := func() {
		if !restored {
			restored = true
			term.Restore(fd, oldState) //nolint:errcheck
		}
	}
	defer restoreOnce()

	type readResult struct {
		b   byte
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := os.Stdin.Read(buf)
		if err != nil {
			ch <- readResult{0, err}
		} else {
			ch <- readResult{buf[0], nil}
		}
	}()

	var b byte
	select {
	case r := <-ch:
		if r.err != nil {
			fmt.Fprintln(os.Stderr)
			return
		}
		b = r.b
	case <-time.After(30 * time.Second):
		fmt.Fprintln(os.Stderr)
		return
	}

	// Restore terminal before running the sub-command so it gets a sane TTY.
	restoreOnce()
	fmt.Fprintln(os.Stderr)

	// Map keypress to hint index ('1' → 0, '2' → 1, …).
	if b < '1' || int(b-'1') >= len(hints) {
		return
	}
	h := hints[int(b-'1')]
	if len(h.Command) == 0 {
		return
	}

	cmd := exec.Command(h.Command[0], h.Command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	os.Exit(exitCode)
}
