package receiver

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

type step struct {
	wait    *api.ReceiverWait
	err     error
	advance time.Duration
}

type fakeControl struct {
	steps    []step
	i        int
	versions []string
	now      *time.Time
}

func (f *fakeControl) WaitEstateReceiver(_ context.Context, _, version string) (*api.ReceiverWait, error) {
	f.versions = append(f.versions, version)
	if f.i >= len(f.steps) {
		return nil, &api.APIError{StatusCode: http.StatusForbidden, Message: "end of script"}
	}
	s := f.steps[f.i]
	f.i++
	*f.now = f.now.Add(s.advance)
	return s.wait, s.err
}

func run(t *testing.T, steps []step) (acts []string, versions []string, err error) {
	t.Helper()
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := &fakeControl{steps: steps, now: &now}
	r := &Receiver{Control: f, InstallationKey: "dev", Retry: time.Millisecond, now: func() time.Time { return now }}
	r.Act = func(context.Context) error { acts = append(acts, now.Format("15:04")); return nil }
	err = r.Run(context.Background())
	return acts, f.versions, err
}

func TestReceiverActsOnFirstContactOnChangeAndOnTheFallback(t *testing.T) {
	acts, versions, err := run(t, []step{
		{wait: &api.ReceiverWait{Version: "v1"}},                                      // first contact: act
		{wait: &api.ReceiverWait{Version: "v1"}, advance: time.Minute},                // unchanged: nothing
		{wait: &api.ReceiverWait{Version: "v2", Changed: true}, advance: time.Minute}, // change: act
		{wait: &api.ReceiverWait{Version: "v2"}, advance: 4 * time.Minute},            // 4 min since: nothing
		{wait: &api.ReceiverWait{Version: "v2"}, advance: 2 * time.Minute},            // 6 min since: fallback
	})
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("the script ends with a refused key; got %v", err)
	}
	want := []string{"09:00", "09:02", "09:08"}
	if len(acts) != len(want) {
		t.Fatalf("acts at %v, want %v", acts, want)
	}
	for i := range want {
		if acts[i] != want[i] {
			t.Fatalf("acts at %v, want %v", acts, want)
		}
	}
	if versions[1] != "v1" || versions[3] != "v2" {
		t.Errorf("each wait carries the last version seen: %v", versions)
	}
}

func TestReceiverStillChecksEveryFiveMinutesWhileTelaraIsUnreachable(t *testing.T) {
	down := errors.New("connection refused")
	acts, _, _ := run(t, []step{
		{wait: &api.ReceiverWait{Version: "v1"}},
		{err: down, advance: 2 * time.Minute},
		{err: down, advance: 4 * time.Minute}, // 6 min since the last act
	})
	if len(acts) != 2 || acts[1] != "09:06" {
		t.Fatalf("acts at %v, want a fallback act at 09:06", acts)
	}
}

func TestReceiverStopsWhenItsKeyIsRefused(t *testing.T) {
	_, _, err := run(t, nil)
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("got %v", err)
	}
}
