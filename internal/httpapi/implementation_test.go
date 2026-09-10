package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func TestStartImplementationHandlerStartsAsynchronously(t *testing.T) {
	now := time.Date(2026, time.September, 10, 3, 0, 0, 0, time.UTC)
	run := execution.Run{
		ID: "run_implementation", FeatureID: "fea_implementation",
		Status: execution.RunStatusRunning, Reason: "The lead is implementing the agreed plan.",
		StartedAt: now, UpdatedAt: now,
	}
	starter := &planningStarterStub{run: run}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_implementation/implementation", nil,
	)
	request.Header.Set("Idempotency-Key", "start-implementation-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if starter.receivedRunID != run.ID || starter.receivedKey != "start-implementation-1" {
		t.Fatalf("unexpected implementation request run=%q key=%q", starter.receivedRunID, starter.receivedKey)
	}
	if recorder.Header().Get("Location") != "/api/v1/runs/"+run.ID {
		t.Fatalf("unexpected Location %q", recorder.Header().Get("Location"))
	}
}

func TestStartImplementationHandlerMapsSafetyFailures(t *testing.T) {
	starter := &planningStarterStub{err: orchestration.ErrImplementationNotAllowed}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)

	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_implementation/implementation", nil,
	))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key to return 400, got %d", missingKey.Code)
	}

	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_implementation/implementation", nil,
	)
	request.Header.Set("Idempotency-Key", "start-implementation-1")
	notReady := httptest.NewRecorder()
	handler.ServeHTTP(notReady, request)
	if notReady.Code != http.StatusConflict {
		t.Fatalf("expected unsafe state to return 409, got %d: %s", notReady.Code, notReady.Body.String())
	}

	starter.err = project.ErrForgejoUnavailable
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, request)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable Forgejo to return 503, got %d", unavailable.Code)
	}

	starter.err = errors.New("database unavailable")
	internal := httptest.NewRecorder()
	handler.ServeHTTP(internal, request)
	if internal.Code != http.StatusInternalServerError {
		t.Fatalf("expected unexpected failure to return 500, got %d", internal.Code)
	}
}
