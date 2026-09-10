package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
)

type planningStarterStub struct {
	run           execution.Run
	err           error
	receivedRunID string
	receivedKey   string
}

func (stub *planningStarterStub) StartPlanning(
	_ context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	stub.receivedRunID = runID
	stub.receivedKey = idempotencyKey
	return stub.run, true, stub.err
}

func (stub *planningStarterStub) StartPlanningReview(
	_ context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	stub.receivedRunID = runID
	stub.receivedKey = idempotencyKey
	return stub.run, true, stub.err
}

func (stub *planningStarterStub) StartPlanningRound(
	_ context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	stub.receivedRunID = runID
	stub.receivedKey = idempotencyKey
	return stub.run, true, stub.err
}

func (stub *planningStarterStub) StartImplementation(
	_ context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	stub.receivedRunID = runID
	stub.receivedKey = idempotencyKey
	return stub.run, true, stub.err
}

type planningExecutionStub struct {
	ExecutionService
	sessions []execution.Session
}

func (stub planningExecutionStub) SessionsForRun(
	context.Context,
	string,
) ([]execution.Session, error) {
	return stub.sessions, nil
}

func TestStartPlanningHandlerStartsAsynchronously(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	run := execution.Run{
		ID: "run_planning", FeatureID: "fea_planning",
		Status: execution.RunStatusRunning, Reason: "The lead is planning.",
		StartedAt: now, UpdatedAt: now,
	}
	starter := &planningStarterStub{run: run}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil,
		planningExecutionStub{sessions: []execution.Session{}},
		nil, nil, nil, starter,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_planning/planning", nil)
	request.Header.Set("Idempotency-Key", "start-planning-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if starter.receivedRunID != run.ID || starter.receivedKey != "start-planning-1" {
		t.Fatalf("unexpected planning request run=%q key=%q", starter.receivedRunID, starter.receivedKey)
	}
	if recorder.Header().Get("Location") != "/api/v1/runs/"+run.ID {
		t.Fatalf("unexpected Location %q", recorder.Header().Get("Location"))
	}
	response := runResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode planning response: %v", err)
	}
	if response.ID != run.ID || response.Status != execution.RunStatusRunning {
		t.Fatalf("unexpected planning response %+v", response)
	}
}

func TestStartPlanningHandlerRequiresReadyStateAndIdempotencyKey(t *testing.T) {
	starter := &planningStarterStub{err: orchestration.ErrPlanningNotAllowed}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)

	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_planning/planning", nil,
	))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key to return 400, got %d", missingKey.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_planning/planning", nil)
	request.Header.Set("Idempotency-Key", "start-planning-1")
	notReady := httptest.NewRecorder()
	handler.ServeHTTP(notReady, request)
	if notReady.Code != http.StatusConflict {
		t.Fatalf("expected unready planning to return 409, got %d: %s", notReady.Code, notReady.Body.String())
	}

	starter.err = execution.ErrNotFound
	missingRun := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/runs/missing/planning", nil)
	request.Header.Set("Idempotency-Key", "start-planning-2")
	handler.ServeHTTP(missingRun, request)
	if missingRun.Code != http.StatusNotFound {
		t.Fatalf("expected missing run to return 404, got %d", missingRun.Code)
	}

	starter.err = errors.New("database unavailable")
	internal := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_planning/planning", nil)
	request.Header.Set("Idempotency-Key", "start-planning-3")
	handler.ServeHTTP(internal, request)
	if internal.Code != http.StatusInternalServerError {
		t.Fatalf("expected unexpected failure to return 500, got %d", internal.Code)
	}
}

func TestStartPlanningReviewHandlerStartsReviewer(t *testing.T) {
	now := time.Date(2026, time.September, 9, 21, 0, 0, 0, time.UTC)
	run := execution.Run{
		ID: "run_review", FeatureID: "fea_planning", Status: execution.RunStatusRunning,
		Reason:    "The reviewer is inspecting the lead's planning proposal.",
		StartedAt: now, UpdatedAt: now,
	}
	starter := &planningStarterStub{run: run}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_review/planning/reviewer", nil)
	request.Header.Set("Idempotency-Key", "start-reviewer-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if starter.receivedRunID != run.ID || starter.receivedKey != "start-reviewer-1" {
		t.Fatalf("unexpected reviewer request run=%q key=%q", starter.receivedRunID, starter.receivedKey)
	}
}

func TestStartPlanningRoundHandlerStartsBoundedPlanningLoop(t *testing.T) {
	now := time.Date(2026, time.September, 9, 22, 0, 0, 0, time.UTC)
	run := execution.Run{
		ID: "run_round", FeatureID: "fea_planning", Status: execution.RunStatusRunning,
		Reason: "The lead is revising the plan.", StartedAt: now, UpdatedAt: now,
	}
	starter := &planningStarterStub{run: run}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_round/planning/round", nil)
	request.Header.Set("Idempotency-Key", "planning-round-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if starter.receivedRunID != run.ID || starter.receivedKey != "planning-round-1" {
		t.Fatalf("unexpected planning round request run=%q key=%q", starter.receivedRunID, starter.receivedKey)
	}
}

func TestStartPlanningRoundHandlerRequiresReadyStateAndIdempotencyKey(t *testing.T) {
	starter := &planningStarterStub{err: orchestration.ErrPlanningNotAllowed}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)

	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_round/planning/round", nil,
	))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key to return 400, got %d", missingKey.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_round/planning/round", nil)
	request.Header.Set("Idempotency-Key", "planning-round-1")
	notReady := httptest.NewRecorder()
	handler.ServeHTTP(notReady, request)
	if notReady.Code != http.StatusConflict {
		t.Fatalf("expected unready round to return 409, got %d: %s", notReady.Code, notReady.Body.String())
	}
}
