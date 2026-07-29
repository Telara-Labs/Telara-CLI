package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The everything server is the reference MCP implementation used for protocol
// coverage. It matters here for one reason the filesystem server cannot cover:
// it emits an unsolicited server->client notification before the initialize
// response, so it exercises frames the client never asked for.
func requireEverythingServer(t *testing.T) (string, []string) {
	t.Helper()
	if os.Getenv("RELAY_SPIKE") == "0" {
		t.Skip("RELAY_SPIKE=0")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx not available; the relay spike requires a real MCP server")
	}
	return npx, []string{"-y", "@modelcontextprotocol/server-everything@latest"}
}

// liveSession holds one relayed MCP session open so requests can be paced,
// rather than dumped in at once. This is what separates real per-call latency
// from server boot time — feeding every frame up front makes each measurement
// include start-up and produces the uniform, meaningless numbers the first
// latency harness reported.
type liveSession struct {
	t        *testing.T
	inWriter *io.PipeWriter
	out      *bufio.Reader
	stats    chan Stats
	cancel   context.CancelFunc
}

func startSession(t *testing.T, command string, args []string, observer Observer) *liveSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	relay := &Relay{Command: command, Args: args, Observer: observer, ServerStderr: &bytes.Buffer{}}
	stats := make(chan Stats, 1)
	go func() {
		s, _ := relay.Run(ctx, inReader, outWriter)
		outWriter.Close()
		stats <- s
	}()

	return &liveSession{t: t, inWriter: inWriter, out: bufio.NewReaderSize(outReader, 1<<20), stats: stats, cancel: cancel}
}

// roundTrip sends one request and waits for the response carrying its id,
// forwarding over any server-initiated notifications that arrive in between.
func (s *liveSession) roundTrip(id int, payload string) (time.Duration, []byte) {
	s.t.Helper()
	start := time.Now()
	if _, err := s.inWriter.Write([]byte(payload + "\n")); err != nil {
		s.t.Fatalf("write request %d: %v", id, err)
	}
	for {
		line, err := s.out.ReadBytes('\n')
		if err != nil {
			s.t.Fatalf("read response %d: %v", id, err)
		}
		var probe struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		if len(probe.ID) > 0 && strings.TrimSpace(string(probe.ID)) == fmt.Sprint(id) {
			return time.Since(start), line
		}
		// A notification or an out-of-order frame. Keep reading.
	}
}

func (s *liveSession) notify(payload string) {
	s.t.Helper()
	if _, err := s.inWriter.Write([]byte(payload + "\n")); err != nil {
		s.t.Fatalf("write notification: %v", err)
	}
}

func (s *liveSession) close() Stats {
	s.inWriter.Close()
	select {
	case st := <-s.stats:
		s.cancel()
		return st
	case <-time.After(30 * time.Second):
		s.cancel()
		s.t.Fatal("relay did not finish after client input closed")
		return Stats{}
	}
}

func initializeFrames() (string, string) {
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"telara-relay-spike","version":"0.1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`
}

// TestRelaySteadyStateLatency measures what the relay actually costs per call,
// with server boot excluded. This is the number G1's p95 gate needs; the earlier
// session-wall-clock measurement could not produce it.
func TestRelaySteadyStateLatency(t *testing.T) {
	command, args := requireEverythingServer(t)

	measure := func(observer Observer) []time.Duration {
		session := startSession(t, command, args, observer)
		defer session.close()

		initFrame, initializedFrame := initializeFrames()
		if _, resp := session.roundTrip(1, initFrame); !bytes.Contains(resp, []byte("protocolVersion")) {
			t.Fatalf("initialize did not complete: %s", truncate(string(resp)))
		}
		session.notify(initializedFrame)

		const calls = 12
		durations := make([]time.Duration, 0, calls)
		for i := 0; i < calls; i++ {
			id := 100 + i
			payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"echo","arguments":{"message":"steady-%d"}}}`, id, i)
			elapsed, resp := session.roundTrip(id, payload)
			if !bytes.Contains(resp, []byte("Echo:")) {
				t.Fatalf("unexpected echo response: %s", truncate(string(resp)))
			}
			durations = append(durations, elapsed)
		}
		return durations
	}

	// Warm the npx/node path once so the comparison is not dominated by install.
	_ = measure(nil)

	withoutObserver := measure(nil)
	withObserver := measure(ObserverFunc(func(Observation) {}))

	p50Plain, p95Plain := percentile(withoutObserver, 50), percentile(withoutObserver, 95)
	p50Obs, p95Obs := percentile(withObserver, 50), percentile(withObserver, 95)

	t.Logf("steady-state per-call round trip through the relay (echo, %d calls):", len(withoutObserver))
	t.Logf("  observation off: p50 %v  p95 %v", p50Plain, p95Plain)
	t.Logf("  observation on : p50 %v  p95 %v", p50Obs, p95Obs)
	t.Logf("  observation cost at p50: %v", p50Obs-p50Plain)

	// The relay is an in-process byte copy plus a small JSON parse. A per-call
	// p95 in the tens of milliseconds would mean something is structurally wrong
	// — a missing flush, or blocking on the observer.
	if p95Obs > 100*time.Millisecond {
		t.Fatalf("p95 per-call latency %v is far above what a byte-forwarding relay should cost", p95Obs)
	}
}

// TestRelayForwardsServerInitiatedNotifications. The everything server sends
// notifications/tools/list_changed unprompted. A relay that only understood
// request/response pairing would drop or stall on it, and the client would
// silently lose server-pushed state.
func TestRelayForwardsServerInitiatedNotifications(t *testing.T) {
	command, args := requireEverythingServer(t)

	var (
		mu       sync.Mutex
		observed []Observation
	)
	session := startSession(t, command, args, ObserverFunc(func(o Observation) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, o)
	}))

	initFrame, initializedFrame := initializeFrames()
	session.roundTrip(1, initFrame)
	session.notify(initializedFrame)
	// tools/list provokes the server's list_changed notification path.
	session.roundTrip(2, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	stats := session.close()

	mu.Lock()
	defer mu.Unlock()

	var sawNotification bool
	for _, o := range observed {
		if o.Direction == FromServer && o.Method == "notifications/tools/list_changed" {
			sawNotification = true
		}
	}
	if !sawNotification {
		var methods []string
		for _, o := range observed {
			methods = append(methods, string(o.Direction)+":"+o.Method)
		}
		t.Fatalf("server-initiated notification was not relayed or observed; saw %v", methods)
	}
	if stats.ServerFrames < 3 {
		t.Fatalf("expected at least initialize, notification and tools/list frames, got %d", stats.ServerFrames)
	}
	t.Logf("relayed %d server frames including an unsolicited notification", stats.ServerFrames)
}

// TestRelayHandlesLargePayloads. Tool results can be large; a relay that read
// frames with a fixed-size scanner would truncate them and corrupt the stream.
func TestRelayHandlesLargePayloads(t *testing.T) {
	command, args := requireEverythingServer(t)

	session := startSession(t, command, args, nil)
	initFrame, initializedFrame := initializeFrames()
	session.roundTrip(1, initFrame)
	session.notify(initializedFrame)

	// A payload comfortably beyond bufio.Scanner's 64KB default token limit.
	large := strings.Repeat("A", 300_000)
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": "echo", "arguments": map[string]any{"message": large}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, resp := session.roundTrip(7, string(payload))
	session.close()

	if len(resp) < 300_000 {
		t.Fatalf("large response was truncated: got %d bytes", len(resp))
	}
	if !bytes.Contains(resp, []byte(strings.Repeat("A", 1000))) {
		t.Fatal("large payload did not survive the relay intact")
	}
	t.Logf("relayed a %d byte response intact", len(resp))
}

func percentile(values []time.Duration, p int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p * (len(sorted) - 1)) / 100
	return sorted[idx]
}
