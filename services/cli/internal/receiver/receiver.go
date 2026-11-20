// Package receiver is the laptop remediation receiver (TENG-3921, s4-01).
// Block telara-documentation/architecture/estate-remediation.md ("Device
// receiver").
//
// The daily telara scan is what takes a blocked MCP server or a removed skill
// off this device. Waiting up to a day for it makes a takedown slow to reach the
// laptop, so the receiver holds an outbound connection to Telara (a long poll
// signed by this device's own key) and runs the scan's enforcement as soon as
// what the device enforces changes, and in any case every five minutes as the
// fallback check. It acts under the person's own CLI login and its device key;
// it never decides anything, and it changes nothing the scan would not.
package receiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// Fallback is the longest the receiver goes without acting.
const Fallback = 5 * time.Minute

// Control is the receiver's view of Telara.
type Control interface {
	WaitEstateReceiver(ctx context.Context, installationKey, version string) (*api.ReceiverWait, error)
}

// Receiver runs Act whenever the device's enforcement inputs change.
type Receiver struct {
	Control         Control
	InstallationKey string
	// Act runs the device's enforcement and reports the machine (telara scan).
	Act func(ctx context.Context) error
	// Fallback overrides the five minute fallback check; zero uses Fallback.
	Fallback time.Duration
	// Retry is how long to wait after a failed wait; zero uses 30 seconds.
	Retry time.Duration
	// MaxActs stops the loop after this many acts; zero runs until ctx ends.
	MaxActs int
	Log     io.Writer
	now     func() time.Time
}

// ErrNotBound means Telara does not hold this device key for this device.
var ErrNotBound = errors.New("this device key is not the registered receiver; run telara receiver install")

func (r *Receiver) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
	}
}

func (r *Receiver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// Run holds the wait until ctx ends, MaxActs is reached, or the key is refused.
func (r *Receiver) Run(ctx context.Context) error {
	fallback, retry := r.Fallback, r.Retry
	if fallback <= 0 {
		fallback = Fallback
	}
	if retry <= 0 {
		retry = 30 * time.Second
	}
	version, acts := "", 0
	var lastAct time.Time
	act := func(why string) error {
		r.logf("acting: %s", why)
		lastAct = r.clock()
		if err := r.Act(ctx); err != nil {
			r.logf("act failed: %v", err)
		}
		acts++
		if r.MaxActs > 0 && acts >= r.MaxActs {
			return errStop
		}
		return nil
	}
	for ctx.Err() == nil {
		w, err := r.Control.WaitEstateReceiver(ctx, r.InstallationKey, version)
		if err != nil {
			var apiErr *api.APIError
			if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusUnauthorized) {
				return fmt.Errorf("%w: %v", ErrNotBound, err)
			}
			if ctx.Err() != nil {
				return nil
			}
			r.logf("wait failed, retrying in %s: %v", retry, err)
			if !lastAct.IsZero() && r.clock().Sub(lastAct) < fallback {
				sleep(ctx, retry)
				continue
			}
			if err := act("fallback check while Telara is unreachable"); err != nil {
				return stopped(err)
			}
			sleep(ctx, retry)
			continue
		}
		switch {
		case w.Changed || version == "":
			version = w.Version
			if err := act("enforcement inputs changed (" + w.Version + ")"); err != nil {
				return stopped(err)
			}
		case r.clock().Sub(lastAct) >= fallback:
			if err := act("five minute fallback check"); err != nil {
				return stopped(err)
			}
		}
	}
	return nil
}

var errStop = errors.New("stop")

func stopped(err error) error {
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
