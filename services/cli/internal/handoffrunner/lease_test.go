package handoffrunner

import (
	"context"
	"sync"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// leaseControl records reports from any goroutine and answers stop when told.
type leaseControl struct {
	Control
	mu      sync.Mutex
	reports []string
	stop    bool
}

func (c *leaseControl) ReportHandoffRun(_ context.Context, _, _, status string) (bool, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, status)
	return c.stop, status, nil
}

func (c *leaseControl) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reports)
}

// TENG-3693 B14: Telara ends a claimed run it stops hearing from, so setup
// reports the run as running until the watch takes over, and stops reporting
// once it has.
func TestSetupRenewsTheLeaseUntilTheWatchTakesOver(t *testing.T) {
	prev := leaseRenewal
	leaseRenewal = 10 * time.Millisecond
	t.Cleanup(func() { leaseRenewal = prev })
	control := &leaseControl{}
	r := &Runner{Control: control}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.renewDuringSetup(ctx, cancel, "exec-1", "inc-1")
	deadline := time.Now().Add(2 * time.Second)
	for control.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	done()
	done()
	n := control.count()
	if n < 2 {
		t.Fatalf("%d setup reports", n)
	}
	for _, status := range control.reports {
		if status != api.HandoffRunRunning {
			t.Fatalf("setup reported %s", status)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if control.count() != n {
		t.Fatal("setup kept reporting after the watch took over")
	}
	if ctx.Err() != nil {
		t.Fatal("a live setup was cancelled")
	}
}

func TestSetupStopsWhenTelaraSaysStop(t *testing.T) {
	prev := leaseRenewal
	leaseRenewal = 10 * time.Millisecond
	t.Cleanup(func() { leaseRenewal = prev })
	control := &leaseControl{stop: true}
	r := &Runner{Control: control}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := r.renewDuringSetup(ctx, cancel, "exec-1", "inc-1")
	defer done()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a stop during setup did not cancel the run")
	}
}
