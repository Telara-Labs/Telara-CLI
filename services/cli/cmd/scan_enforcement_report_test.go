package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A scan that could not learn the deny list must not look like a clean
// enforcement to a machine reading --json.
func TestEnforcementReportSaysIncompleteWhenTheDenyListCannotBeFetched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rep := enforceRemovals(srv.URL, "synthetic-token", true)
	if rep == nil || rep.Checked || rep.Complete || len(rep.Errors) == 0 {
		t.Fatalf("a failed fetch must report checked=false complete=false with an error, got %+v", rep)
	}
}

func TestEnforcementReportIsCompleteWhenNothingIsWithdrawn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"denied":[]}`))
	}))
	defer srv.Close()

	rep := enforceRemovals(srv.URL, "synthetic-token", true)
	if rep == nil || !rep.Checked || !rep.Complete || len(rep.Errors) != 0 {
		t.Fatalf("an empty deny list is a complete, clean enforcement, got %+v", rep)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["quarantined"].([]any); !ok {
		t.Errorf("quarantined must serialize as an array, got %s", raw)
	}
}
