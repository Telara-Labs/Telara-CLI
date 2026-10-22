package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/telara-labs/telara-cli/services/cli/internal/api"
)

// The Gateway answers expired and denied with HTTP 400 and {status}. The CLI
// must turn those into its own messages, not a raw API error body (UIX-G07).
func TestPollForTokenReportsExpiredAndDeniedFromHTTP400(t *testing.T) {
	for status, want := range map[string]string{
		"expired": "device code expired",
		"denied":  "authorization was denied",
	} {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := PollForToken(ctx, api.NewClient(srv.URL, ""), "dc", 1)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want it to contain %q", err, want)
			}
			if strings.Contains(err.Error(), "API error") {
				t.Fatalf("the user sees a raw API error: %v", err)
			}
		})
	}
}

// One 429 or 5xx during the wait must not end the login.
func TestPollForTokenSurvivesATransientPollError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
		case 2:
			http.Error(w, `{"error":"bad gateway"}`, http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"status":"complete","token":"tlrc_ok"}`))
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	token, err := PollForToken(ctx, api.NewClient(srv.URL, ""), "dc", 1)
	if err != nil || token != "tlrc_ok" {
		t.Fatalf("token = %q, err = %v; a transient error ended the login", token, err)
	}
}

// A refusal that is not transient still ends the login at once.
func TestPollForTokenStopsOnANonTransientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := PollForToken(ctx, api.NewClient(srv.URL, ""), "dc", 1); err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 1 {
		t.Fatalf("polled %d times after a 403", calls.Load())
	}
}
