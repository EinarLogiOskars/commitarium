package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func TestPauseAndResumeRunHandlersRequireIdempotencyAndReturnState(t *testing.T) {
	now := time.Date(2026, time.September, 12, 10, 0, 0, 0, time.UTC)
	workflow := &planningStarterStub{run: execution.Run{
		ID: "run_control", FeatureID: "fea_control",
		Status: execution.RunStatusWaitingForUser,
		Reason: "Paused after planning.", WaitKind: execution.RunWaitKindPaused, Paused: true,
		StartedAt: now, UpdatedAt: now,
	}}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, workflow,
	)

	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_control/pause", nil,
	))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key to return 400, got %d", missingKey.Code)
	}

	pauseRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_control/pause", nil)
	pauseRequest.Header.Set("Idempotency-Key", "pause-control")
	pauseResponse := httptest.NewRecorder()
	handler.ServeHTTP(pauseResponse, pauseRequest)
	if pauseResponse.Code != http.StatusAccepted || workflow.receivedAction != "pause" ||
		workflow.receivedRunID != "run_control" || workflow.receivedKey != runActionIDForKey("pause-control") {
		t.Fatalf("unexpected pause response/status: code=%d workflow=%+v body=%s", pauseResponse.Code, workflow, pauseResponse.Body.String())
	}
	if body := pauseResponse.Body.String(); !containsAll(body, `"wait_kind":"paused"`, `"paused":true`) {
		t.Fatalf("pause response omitted control state: %s", body)
	}

	workflow.run.Paused = false
	workflow.run.WaitKind = execution.RunWaitKindPhaseCheckpoint
	resumeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_control/resume", nil)
	resumeRequest.Header.Set("Idempotency-Key", "resume-control")
	resumeResponse := httptest.NewRecorder()
	handler.ServeHTTP(resumeResponse, resumeRequest)
	if resumeResponse.Code != http.StatusAccepted || workflow.receivedAction != "resume" ||
		workflow.receivedKey != runActionIDForKey("resume-control") {
		t.Fatalf("unexpected resume response/status: code=%d workflow=%+v body=%s", resumeResponse.Code, workflow, resumeResponse.Body.String())
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
