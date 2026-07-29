// Command relay-spike exposes the TENG-1528 relay as an executable so a real
// MCP client can be pointed at it.
//
// A client cannot be handed a Go library. Without this binary the spike can only
// be driven by synthetic frames written by the same author as the relay, which
// leaves the most important compatibility question — does a real client work
// through it — untested. The client launches this exactly as it would launch the
// server; the real server command is supplied out of band.
//
// Usage:
//
//	RELAY_SPIKE_SERVER='npx -y @modelcontextprotocol/server-everything@latest' relay-spike
//
// Observations are written to the file named by RELAY_SPIKE_OBSERVATIONS, one
// JSON object per line, so a test can assert on what the relay saw. Nothing is
// written to stdout except relayed protocol bytes: stdout is the wire.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/relay"
)

func main() {
	command := strings.Fields(os.Getenv("RELAY_SPIKE_SERVER"))
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "relay-spike: RELAY_SPIKE_SERVER is required")
		os.Exit(2)
	}

	var (
		mu       sync.Mutex
		sink     *os.File
		observer relay.Observer
	)
	if path := os.Getenv("RELAY_SPIKE_OBSERVATIONS"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relay-spike: observations file: %v\n", err)
			os.Exit(2)
		}
		sink = file
		defer sink.Close()
		encoder := json.NewEncoder(sink)
		observer = relay.ObserverFunc(func(o relay.Observation) {
			mu.Lock()
			defer mu.Unlock()
			_ = encoder.Encode(map[string]any{
				"direction":  string(o.Direction),
				"method":     o.Method,
				"id":         o.ID,
				"tool_name":  o.ToolName,
				"is_error":   o.IsError,
				"bytes":      o.Bytes,
				"latency_ms": o.Latency.Seconds() * 1000,
			})
		})
	}

	// A client that gives up mid-session sends a signal; the relay must tear the
	// child down rather than orphan it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	r := &relay.Relay{
		Command:      command[0],
		Args:         command[1:],
		Observer:     observer,
		ServerStderr: os.Stderr,
	}
	if _, err := r.Run(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "relay-spike: %v\n", err)
		os.Exit(1)
	}
}
