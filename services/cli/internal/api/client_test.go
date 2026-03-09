package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIError_Error(t *testing.T) {
	err := &APIError{StatusCode: 404, Message: "not found"}
	want := "API error 404: not found"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q; want %q", got, want)
	}
}

func TestDo_NetworkError(t *testing.T) {
	// Start a server and immediately close it so connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	client := NewClient(srv.URL, "test-token")
	err := client.do(context.Background(), "GET", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected error from closed server, got nil")
	}
}

func TestDo_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "bad request"})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	err := client.do(context.Background(), "GET", "/test", nil, nil)
	if err == nil {
		t.Fatal("expected *APIError, got nil")
	}

	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d; want 400", apiErr.StatusCode)
	}
	if apiErr.Message != "bad request" {
		t.Errorf("Message = %q; want %q", apiErr.Message, "bad request")
	}
	if apiErr.Body == "" {
		t.Error("Body should be populated")
	}
}

func TestDo_Success(t *testing.T) {
	type respBody struct {
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(respBody{Name: "my-config"})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	var out respBody
	if err := client.do(context.Background(), "GET", "/test", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Name != "my-config" {
		t.Errorf("Name = %q; want %q", out.Name, "my-config")
	}
}

func TestExtractJSONMessage_MessageField(t *testing.T) {
	data := []byte(`{"message":"something went wrong"}`)
	got := extractJSONMessage(data)
	if got != "something went wrong" {
		t.Errorf("got %q; want %q", got, "something went wrong")
	}
}

func TestExtractJSONMessage_ErrorField(t *testing.T) {
	data := []byte(`{"error":"unauthorized"}`)
	got := extractJSONMessage(data)
	if got != "unauthorized" {
		t.Errorf("got %q; want %q", got, "unauthorized")
	}
}

func TestExtractJSONMessage_NotJSON(t *testing.T) {
	data := []byte(`not json`)
	got := extractJSONMessage(data)
	if got != "" {
		t.Errorf("expected empty string for non-JSON, got %q", got)
	}
}
