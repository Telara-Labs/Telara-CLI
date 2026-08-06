package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The TENG-1528 scope names four failure modes to exercise: server exits
// mid-session, malformed frames, large payloads, and concurrent in-flight
// requests. The first and third are covered in relay_test.go and
// relay_matrix_test.go respectively. This file closes the remaining two.

// echoServerArgs returns a line-buffered echo "server": every line in is the
// same line out.
//
// A real MCP server is deliberately NOT used for the malformed-frame case. A
// real server may reject garbage by erroring or closing the session, which would
// tell us about the server's error handling rather than the relay's. The
// question here is narrower and is entirely about the relay: does a frame it
// cannot parse still cross unaltered, and does the session survive it?
func echoServerArgs() (string, []string) {
	return "sh", []string{"-c", `while IFS= read -r line; do printf '%s\n' "$line"; done`}
}

// TestRelayForwardsMalformedFramesVerbatim covers the "malformed frames" failure
// mode.
//
// relay.go observes on a best-effort basis: a frame that will not unmarshal is
// forwarded but not described, on the stated grounds that reporting it as a
// resource "would be fabrication". That is the right call, but it is a claim the
// spike had not tested. Two things must hold:
//
//  1. Byte fidelity is unconditional. A relay that drops or rewrites a frame it
//     failed to parse is worse than one that never parsed at all — a client
//     speaking a protocol extension the relay does not model would silently lose
//     traffic.
//  2. A malformed frame must not poison the session. Valid frames after it are
//     still forwarded and still observed.
func TestRelayForwardsMalformedFramesVerbatim(t *testing.T) {
	if os.Getenv("RELAY_SPIKE") == "0" {
		t.Skip("RELAY_SPIKE=0")
	}
	command, args := echoServerArgs()

	// Interleaved deliberately: a valid frame after each malformed one is what
	// proves the session survived rather than merely tolerated a trailing mess.
	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{not json at all`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`plain text, not even an object`,
		`[1,2,3]`, // valid JSON, but an array — unmarshals into the frame struct? no.
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"probe"}}`,
	}
	const validFrames = 3
	input := strings.Join(lines, "\n") + "\n"

	var (
		mu       sync.Mutex
		observed []Observation
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	relay := &Relay{
		Command: command,
		Args:    args,
		Observer: ObserverFunc(func(o Observation) {
			mu.Lock()
			observed = append(observed, o)
			mu.Unlock()
		}),
		ServerStderr: &bytes.Buffer{},
	}
	var out bytes.Buffer
	stats, err := relay.Run(ctx, strings.NewReader(input), &out)
	if err != nil && !contains(err.Error(), "server exited") {
		t.Fatalf("relay returned an error on a stream containing malformed frames: %v", err)
	}

	// (1) Byte fidelity, unconditionally.
	if out.String() != input {
		t.Fatalf("relay altered a stream containing malformed frames.\n--- in (%d bytes) ---\n%s\n--- out (%d bytes) ---\n%s",
			len(input), truncate(input), out.Len(), truncate(out.String()))
	}

	// Every line crossed in both directions.
	if stats.ClientFrames != len(lines) {
		t.Fatalf("relay forwarded %d client frames, want %d — a malformed frame was dropped", stats.ClientFrames, len(lines))
	}
	if stats.ServerFrames != len(lines) {
		t.Fatalf("relay forwarded %d server frames, want %d", stats.ServerFrames, len(lines))
	}

	// (2) Observation is best-effort and must not fabricate. Only the well-formed
	// JSON-RPC objects are describable, and each crosses twice (up and back).
	mu.Lock()
	got := append([]Observation(nil), observed...)
	mu.Unlock()

	described := 0
	for _, o := range got {
		if o.Method != "" || o.ID != "" {
			described++
		}
	}
	if described != validFrames*2 {
		var methods []string
		for _, o := range got {
			methods = append(methods, fmt.Sprintf("%s/%s/%s", o.Direction, o.Method, o.ID))
		}
		t.Fatalf("described %d frames, want %d (%d valid x 2 directions); observations: %v",
			described, validFrames*2, validFrames, methods)
	}

	// The frame after the malformed ones must still be fully described, or the
	// parser state was corrupted by the garbage ahead of it.
	var sawLastCall bool
	for _, o := range got {
		if o.Method == "tools/call" && o.ToolName == "probe" {
			sawLastCall = true
		}
	}
	if !sawLastCall {
		t.Fatal("the tools/call after the malformed frames was not observed; malformed input poisoned the session")
	}

	t.Logf("malformed frames: %d lines relayed byte-identically (%d bytes), %d/%d describable frames observed, session intact",
		len(lines), len(input), described, len(lines)*2)
}

// TestRelayPipelinesConcurrentInFlightRequests covers the "concurrent in-flight
// requests" failure mode against a real MCP server.
//
// relay.go pumps both directions concurrently and its doc comment asserts the
// reason: "A request/response protocol would deadlock if the relay waited for a
// response before forwarding the next request, since MCP clients pipeline."
// That is the single most load-bearing concurrency claim in the relay and it was
// untested — every other test writes one request and waits for its response,
// which a fully serial relay would also pass.
//
// This drives real pipelining: N requests are written back-to-back with no read
// in between, then all N responses are collected.
func TestRelayPipelinesConcurrentInFlightRequests(t *testing.T) {
	command, args := requireEverythingServer(t)

	var (
		mu       sync.Mutex
		observed []Observation
	)
	session := startSession(t, command, args, ObserverFunc(func(o Observation) {
		mu.Lock()
		observed = append(observed, o)
		mu.Unlock()
	}))

	initReq, initNote := initializeFrames()
	session.roundTrip(1, initReq)
	session.notify(initNote)

	// Fire everything at once. No reads until all writes are done — this is the
	// shape that deadlocks a serial relay.
	const inFlight = 8
	const firstID = 100
	start := time.Now()
	for i := 0; i < inFlight; i++ {
		id := firstID + i
		payload := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"echo","arguments":{"message":"pipelined-%d"}}}`,
			id, i)
		session.notify(payload)
	}

	// Collect responses by id. They may arrive in any order, interleaved with
	// server-initiated notifications.
	outstanding := make(map[int]bool, inFlight)
	for i := 0; i < inFlight; i++ {
		outstanding[firstID+i] = true
	}
	deadline := time.After(120 * time.Second)
	done := make(chan error, 1)
	go func() {
		for len(outstanding) > 0 {
			line, err := session.out.ReadBytes('\n')
			if err != nil {
				done <- fmt.Errorf("read while %d responses outstanding: %w", len(outstanding), err)
				return
			}
			var probe struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(line, &probe) != nil || len(probe.ID) == 0 {
				continue
			}
			var id int
			if json.Unmarshal(probe.ID, &id) != nil {
				continue
			}
			delete(outstanding, id)
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pipelined requests did not all complete: %v", err)
		}
	case <-deadline:
		var missing []int
		for id := range outstanding {
			missing = append(missing, id)
		}
		sort.Ints(missing)
		t.Fatalf("relay stalled with %d requests in flight; ids %v never answered. "+
			"This is the deadlock the concurrent pump exists to prevent", len(missing), missing)
	}
	elapsed := time.Since(start)

	stats := session.close()

	// Latency attribution must survive pipelining: ids are matched out of order,
	// so a naive single-slot pending map would lose most of them.
	if len(stats.Latencies) < inFlight {
		t.Fatalf("matched %d request/response latencies, want at least %d — "+
			"pipelined ids were lost by the pending-request map", len(stats.Latencies), inFlight)
	}

	mu.Lock()
	total := len(observed)
	mu.Unlock()

	t.Logf("pipelined %d concurrent tools/call requests, all answered in %v; "+
		"%d latencies attributed, %d frames observed", inFlight, elapsed, len(stats.Latencies), total)
}
