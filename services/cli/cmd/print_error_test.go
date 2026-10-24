package cmd

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// logout's "the token is still valid on the server" statement must reach the
// user even when the cause is a network failure.
func TestPrintErrorKeepsTheStillValidStatementOnNetworkFailure(t *testing.T) {
	err := stillValidError(errors.New("the token is still valid on the server, the revoke failed. dial tcp 127.0.0.1:1: connect: connection refused"))
	out := captureStderr(t, func() { printError(err) })
	if !strings.Contains(out, "still valid on the server") {
		t.Fatalf("the statement was swallowed: %q", out)
	}
}

// Other commands keep the generic hint.
func TestPrintErrorKeepsTheGenericHintForOtherNetworkFailures(t *testing.T) {
	out := captureStderr(t, func() { printError(errors.New("Post x: dial tcp 127.0.0.1:1: connect: connection refused")) })
	if !strings.Contains(out, "Could not reach the Telara API") {
		t.Fatalf("generic hint lost: %q", out)
	}
}
