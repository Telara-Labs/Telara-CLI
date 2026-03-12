package display

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"golang.org/x/term"
)

var (
	ColorBrand   = color.New(color.FgMagenta, color.Bold)
	ColorSuccess = color.New(color.FgGreen, color.Bold)
	ColorError   = color.New(color.FgRed, color.Bold)
	ColorWarn    = color.New(color.FgYellow)
	ColorCmd     = color.New(color.FgCyan, color.Bold)
	ColorDim     = color.New(color.Faint)
	ColorBold    = color.New(color.Bold)
)

const (
	IconCheck = "\u2713" // ✓
	IconCross = "\u2717" // ✗
	IconWarn  = "\u25b2" // ▲
	IconArrow = "\u203a" // ›
	IconDash  = "\u2500" // ─
)

var (
	stdoutTTYOnce sync.Once
	stderrTTYOnce sync.Once
	stdoutTTY     bool
	stderrTTY     bool
)

// StdoutIsTTY reports whether stdout is connected to a real terminal.
func StdoutIsTTY() bool {
	stdoutTTYOnce.Do(func() {
		stdoutTTY = term.IsTerminal(int(os.Stdout.Fd()))
	})
	return stdoutTTY
}

// StderrIsTTY reports whether stderr is connected to a real terminal.
func StderrIsTTY() bool {
	stderrTTYOnce.Do(func() {
		stderrTTY = term.IsTerminal(int(os.Stderr.Fd()))
	})
	return stderrTTY
}

// PrintSuccess writes "  ✓ msg" in green to stdout.
func PrintSuccess(msg string) {
	fmt.Fprintln(os.Stdout, "  "+ColorSuccess.Sprint(IconCheck)+" "+msg)
}

// PrintError writes "  ✗ msg" in red to stderr.
func PrintError(msg string) {
	fmt.Fprintln(os.Stderr, "  "+ColorError.Sprint(IconCross)+" "+msg)
}

// PrintWarn writes "  ▲ msg" in yellow to stdout.
func PrintWarn(msg string) {
	fmt.Fprintln(os.Stdout, "  "+ColorWarn.Sprint(IconWarn)+" "+msg)
}

// PrintInfo writes "  • msg" in dim to stdout.
func PrintInfo(msg string) {
	fmt.Fprintln(os.Stdout, "  "+ColorDim.Sprint("•")+" "+msg)
}

// PrintSection writes a bold title followed by a dim divider to stdout.
func PrintSection(title string) {
	fmt.Fprintln(os.Stdout, ColorBold.Sprint(title))
	fmt.Fprintln(os.Stdout, ColorDim.Sprint(strings.Repeat(IconDash, 40)))
}

// PrintKV writes a dim label and normal value pair to w.
// The label is padded to 14 characters.
func PrintKV(w io.Writer, label, value string) {
	paddedLabel := fmt.Sprintf("%-14s", label)
	fmt.Fprintf(w, "  %s%s\n", ColorDim.Sprint(paddedLabel), value)
}

// PrintKVHighlight writes a dim label and cyan-highlighted value pair to w.
func PrintKVHighlight(w io.Writer, label, value string) {
	paddedLabel := fmt.Sprintf("%-14s", label)
	fmt.Fprintf(w, "  %s%s\n", ColorDim.Sprint(paddedLabel), ColorCmd.Sprint(value))
}

// ─── Spinner ────────────────────────────────────────────────────────────────

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner animates a progress indicator on stderr while work is in progress.
// It is a no-op when stderr is not a real TTY.
type Spinner struct {
	mu     sync.Mutex
	msg    string
	active bool
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewSpinner creates a new Spinner.
func NewSpinner() *Spinner {
	return &Spinner{}
}

// Start begins the animation with msg as the label.
// On non-TTY stderr it prints a plain "  msg" line once instead.
func (s *Spinner) Start(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return
	}
	s.msg = msg

	if !StderrIsTTY() {
		fmt.Fprintf(os.Stderr, "  %s\n", msg)
		return
	}

	s.active = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})

	go func() {
		defer close(s.doneCh)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.mu.Lock()
				currentMsg := s.msg
				s.mu.Unlock()
				frame := spinnerFrames[i%len(spinnerFrames)]
				fmt.Fprintf(os.Stderr, "\r  %s %s", ColorCmd.Sprint(frame), currentMsg)
				i++
			}
		}
	}()
}

// Stop clears the spinner line silently. Safe to call multiple times.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	stopCh := s.stopCh
	doneCh := s.doneCh
	s.mu.Unlock()

	close(stopCh)
	<-doneCh
	// Erase the spinner line.
	fmt.Fprintf(os.Stderr, "\r%-80s\r", "")
}

// Success stops the spinner and prints "  ✓ msg" to stderr.
func (s *Spinner) Success(msg string) {
	s.Stop()
	fmt.Fprintln(os.Stderr, "  "+ColorSuccess.Sprint(IconCheck)+" "+msg)
}

// Fail stops the spinner and prints "  ✗ msg" to stderr.
func (s *Spinner) Fail(msg string) {
	s.Stop()
	fmt.Fprintln(os.Stderr, "  "+ColorError.Sprint(IconCross)+" "+msg)
}

// UpdateMessage changes the spinner label while it is running.
func (s *Spinner) UpdateMessage(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()
}
