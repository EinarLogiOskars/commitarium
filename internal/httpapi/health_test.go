package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, nil, nil).ServeHTTP(recorder, req)

	res := recorder.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.StatusCode)
	}

	if contentType := res.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	var status Status
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Fatalf("could not decode response: %v", err)
	}

	if status.Status != "ok" {
		t.Errorf("expected status %q. got %q", "ok", status.Status)
	}

}

func TestHealthHandlerRejectsPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, nil, nil).ServeHTTP(recorder, req)

	res := recorder.Result()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, res.StatusCode)
	}

}
