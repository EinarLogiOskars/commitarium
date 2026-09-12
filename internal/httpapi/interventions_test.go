package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestQueueInterventionHandlerArmsPauseAndReturnsObservableState(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 30, 0, 0, time.UTC)
	run := execution.Run{
		ID: "run_intervene", FeatureID: "fea_intervene",
		Status: execution.RunStatusRunning, WaitKind: execution.RunWaitKindPaused,
		Paused: true, StartedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	intervention := execution.Intervention{
		ID: interventionIDForKey("intervene-1"), RunID: run.ID, SessionID: "ses_lead",
		Target: worker.RoleLead, Message: "Please reconsider the storage format.",
		Status:      execution.InterventionStatusWaitingForBoundary,
		RequestedAt: now, UpdatedAt: now,
	}
	executions := &recordingExecutionService{
		run: run, intervention: intervention,
		interventionTargets: []execution.InterventionTarget{{
			Role: worker.RoleLead, SessionID: "ses_lead",
		}},
	}
	workflow := &planningStarterStub{run: run, intervention: intervention}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, executions, nil, nil, nil, workflow,
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runs/run_intervene/interventions",
		strings.NewReader(`{"target":"lead","message":"Please reconsider the storage format."}`),
	)
	request.Header.Set("Idempotency-Key", "intervene-1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", response.Code, response.Body.String())
	}
	if workflow.receivedRunID != run.ID || workflow.receivedKey != intervention.ID ||
		workflow.receivedTarget != worker.RoleLead || workflow.receivedMessage != intervention.Message {
		t.Fatalf("unexpected intervention request %+v", workflow)
	}
	if response.Header().Get("Location") != "/api/v1/runs/"+run.ID {
		t.Fatalf("unexpected Location %q", response.Header().Get("Location"))
	}
	if body := response.Body.String(); !containsAll(
		body,
		`"paused":true`,
		`"wait_kind":"paused"`,
		`"intervention_targets":[{"role":"lead","session_id":"ses_lead"}]`,
		`"intervention":{"id":"`+intervention.ID+`"`,
		`"status":"waiting_for_boundary"`,
	) {
		t.Fatalf("intervention response omitted state: %s", body)
	}
}

func TestQueueInterventionHandlerValidatesRequest(t *testing.T) {
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, &recordingExecutionService{}, nil, nil, nil, &planningStarterStub{},
	)
	tests := []struct {
		name string
		key  string
		body string
	}{
		{name: "missing idempotency key", body: `{"target":"lead","message":"hello"}`},
		{name: "unknown target", key: "invalid-target", body: `{"target":"coder","message":"hello"}`},
		{name: "blank message", key: "blank-message", body: `{"target":"lead","message":"  "}`},
		{name: "unknown field", key: "unknown-field", body: `{"target":"lead","message":"hello","extra":true}`},
		{name: "second document", key: "second-document", body: `{"target":"lead","message":"hello"}{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost, "/api/v1/runs/run_test/interventions", strings.NewReader(test.body),
			)
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestQueueInterventionHandlerMapsStableConflicts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		kind string
	}{
		{name: "missing run", err: execution.ErrNotFound, code: http.StatusNotFound, kind: "run_not_found"},
		{name: "idempotency conflict", err: execution.ErrInterventionConflict, code: http.StatusConflict, kind: "idempotency_conflict"},
		{name: "unfinished request", err: execution.ErrInterventionInProgress, code: http.StatusConflict, kind: "intervention_in_progress"},
		{name: "missing target", err: execution.ErrInterventionTargetUnavailable, code: http.StatusConflict, kind: "intervention_target_unavailable"},
		{name: "terminal run", err: execution.ErrInterventionNotAllowed, code: http.StatusConflict, kind: "intervention_not_allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflow := &planningStarterStub{err: test.err}
			handler := NewWithWorkspaceAndRealWorkflowService(
				nil, nil, nil, &recordingExecutionService{}, nil, nil, nil, workflow,
			)
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/runs/run_test/interventions",
				strings.NewReader(`{"target":"reviewer","message":"Please inspect this concern."}`),
			)
			request.Header.Set("Idempotency-Key", "conflict-test")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.code || !strings.Contains(response.Body.String(), `"code":"`+test.kind+`"`) {
				t.Fatalf("expected %d/%s, got %d: %s", test.code, test.kind, response.Code, response.Body.String())
			}
		})
	}
}

func TestResumeRunHandlerReportsPendingIntervention(t *testing.T) {
	workflow := &planningStarterStub{err: orchestration.ErrInterventionPending}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, &recordingExecutionService{}, nil, nil, nil, workflow,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_test/resume", nil)
	request.Header.Set("Idempotency-Key", "resume-with-intervention")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"intervention_pending"`) {
		t.Fatalf("unexpected pending-intervention response %d: %s", response.Code, response.Body.String())
	}
}

func TestResumeRunHandlerReportsUnresolvedInterventionEffect(t *testing.T) {
	workflow := &planningStarterStub{err: orchestration.ErrInterventionResolutionPending}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, &recordingExecutionService{}, nil, nil, nil, workflow,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_test/resume", nil)
	request.Header.Set("Idempotency-Key", "resume-with-unresolved-intervention")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"intervention_resolution_pending"`) {
		t.Fatalf("unexpected unresolved-intervention response %d: %s", response.Code, response.Body.String())
	}
}
