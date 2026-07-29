// Package relay is the TENG-1528 local MCP relay compatibility spike.
//
// It is a feasibility probe for the Local Endpoint Relay (W1/TENG-1492), the
// mediation plane the AI Estate commercial sequence is built on. Stop rule #1 of
// that sequence retires the wedge if a relay cannot carry real MCP traffic
// without breaking protocol behaviour or developer workflow, so this package
// exists to answer that question with measurements rather than argument.
//
// It is deliberately NOT: signed, policy-enforcing, publishing to the estate,
// managed, or distributed. Those are W1 delivery. Adding them here would make
// the spike unable to fail cleanly.
//
// The single most important property is byte fidelity. The relay forwards the
// exact bytes it received and never re-serialises a frame, because
// re-marshalling JSON reorders object keys and rewrites number formatting — the
// message would stay semantically equal while ceasing to be identical, and any
// client doing signature or hash verification over the raw frame would break.
package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Direction records which way a frame was travelling.
type Direction string

const (
	// FromClient is a frame the client sent toward the server.
	FromClient Direction = "client->server"
	// FromServer is a frame the server sent back toward the client.
	FromServer Direction = "server->client"
)

// Observation is metadata about one relayed frame.
//
// It carries no prompt text, tool arguments, result content, tokens or
// credentials. That is a hard constraint of the estate contract, and it is
// enforced structurally: this struct has nowhere to put them.
type Observation struct {
	Direction Direction
	// Method is the JSON-RPC method, empty on responses.
	Method string
	// ID is the JSON-RPC id rendered as a string, empty on notifications.
	ID string
	// ToolName is params.name for tools/call. The tool invoked is governance
	// signal; its arguments are not.
	ToolName string
	// IsError reports a JSON-RPC error response.
	IsError bool
	// Bytes is the frame size. Useful for capacity work, and it is a count, not
	// content.
	Bytes int
	// Latency is set on a response that matched an earlier request id.
	Latency time.Duration
	At      time.Time
}

// Observer receives observations. Implementations must not block.
type Observer interface {
	Record(Observation)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Observation)

func (f ObserverFunc) Record(o Observation) { f(o) }

// Relay proxies a client's stdio session to a child MCP server process.
type Relay struct {
	// Command and Args launch the server exactly as a client would.
	Command string
	Args    []string
	Env     []string
	// Observer is optional; a nil Observer disables observation entirely, which
	// is what the fidelity comparison runs against.
	Observer Observer
	// ServerStderr receives the child's stderr. MCP servers log there, and
	// swallowing it would hide real failures during a compatibility spike.
	ServerStderr io.Writer

	mu        sync.Mutex
	pending   map[string]time.Time
	latencies []time.Duration
}

// Stats summarises a completed session.
type Stats struct {
	ClientFrames int
	ServerFrames int
	Latencies    []time.Duration
}

// Run relays until the client input ends or the server exits.
//
// Both directions are pumped concurrently. A request/response protocol would
// deadlock if the relay waited for a response before forwarding the next
// request, since MCP clients pipeline.
func (r *Relay) Run(ctx context.Context, clientIn io.Reader, clientOut io.Writer) (Stats, error) {
	if r.Command == "" {
		return Stats{}, errors.New("relay: server command is required")
	}
	r.pending = make(map[string]time.Time)

	cmd := exec.CommandContext(ctx, r.Command, r.Args...)
	if r.Env != nil {
		cmd.Env = r.Env
	}
	cmd.Stderr = r.ServerStderr

	serverIn, err := cmd.StdinPipe()
	if err != nil {
		return Stats{}, fmt.Errorf("relay: server stdin: %w", err)
	}
	serverOut, err := cmd.StdoutPipe()
	if err != nil {
		return Stats{}, fmt.Errorf("relay: server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Stats{}, fmt.Errorf("relay: start server: %w", err)
	}

	var (
		stats   Stats
		statsMu sync.Mutex
		wg      sync.WaitGroup
		upErr   error
		downErr error
	)

	wg.Add(2)

	// Client -> server.
	go func() {
		defer wg.Done()
		// Closing the server's stdin is what tells a well-behaved MCP server to
		// shut down once the client goes away. Without it the child outlives the
		// session and the spike leaks processes.
		defer serverIn.Close()
		n, err := r.pump(clientIn, serverIn, FromClient)
		statsMu.Lock()
		stats.ClientFrames = n
		statsMu.Unlock()
		upErr = err
	}()

	// Server -> client.
	go func() {
		defer wg.Done()
		n, err := r.pump(serverOut, clientOut, FromServer)
		statsMu.Lock()
		stats.ServerFrames = n
		statsMu.Unlock()
		downErr = err
	}()

	wg.Wait()
	waitErr := cmd.Wait()

	r.mu.Lock()
	stats.Latencies = r.latencies
	r.mu.Unlock()

	if upErr != nil {
		return stats, upErr
	}
	if downErr != nil {
		return stats, downErr
	}
	// A server that exits non-zero after a clean relay is a server problem, not
	// a relay problem, but the spike must surface it rather than report success.
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return stats, fmt.Errorf("relay: server exited: %w", waitErr)
		}
	}
	return stats, nil
}

func (r *Relay) pump(src io.Reader, dst io.Writer, direction Direction) (int, error) {
	// bufio.Reader.ReadBytes is used rather than bufio.Scanner because a
	// tools/list response routinely exceeds Scanner's default token limit, and a
	// truncated frame would look like a relay compatibility failure that is
	// actually a bug in the relay's reader.
	reader := bufio.NewReaderSize(src, 64*1024)
	frames := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			frames++
			r.observe(line, direction)
			// Forward the exact bytes, including the delimiter, before handling
			// any read error: a final frame without a trailing newline must
			// still reach the peer.
			if _, writeErr := dst.Write(line); writeErr != nil {
				return frames, fmt.Errorf("relay: forward %s: %w", direction, writeErr)
			}
			if flusher, ok := dst.(interface{ Flush() error }); ok {
				if flushErr := flusher.Flush(); flushErr != nil {
					return frames, fmt.Errorf("relay: flush %s: %w", direction, flushErr)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return frames, nil
			}
			// A closed pipe is the normal end of a session once the peer goes
			// away, not a relay failure.
			if errors.Is(err, io.ErrClosedPipe) {
				return frames, nil
			}
			return frames, nil
		}
	}
}

// frame is the minimal JSON-RPC surface the relay needs. Everything else in the
// message is deliberately left unparsed and unrecorded.
type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
	Error json.RawMessage `json:"error"`
}

func (r *Relay) observe(line []byte, direction Direction) {
	if r.Observer == nil {
		return
	}
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		// Not JSON, or not an object. The frame is still forwarded verbatim; it
		// is simply not describable. Reporting it as a resource would be
		// fabrication.
		return
	}

	id := ""
	if len(f.ID) > 0 && string(f.ID) != "null" {
		id = string(f.ID)
	}

	obs := Observation{
		Direction: direction,
		Method:    f.Method,
		ID:        id,
		ToolName:  f.Params.Name,
		IsError:   len(f.Error) > 0,
		Bytes:     len(line),
		At:        time.Now(),
	}

	// tools/call carries the invoked tool in params.name. Only that name is
	// taken; params.arguments is never touched.
	if f.Method != "tools/call" {
		obs.ToolName = ""
	}

	if id != "" {
		r.mu.Lock()
		if direction == FromClient && f.Method != "" {
			r.pending[id] = obs.At
		} else if direction == FromServer {
			if started, ok := r.pending[id]; ok {
				obs.Latency = obs.At.Sub(started)
				r.latencies = append(r.latencies, obs.Latency)
				delete(r.pending, id)
			}
		}
		r.mu.Unlock()
	}

	r.Observer.Record(obs)
}
