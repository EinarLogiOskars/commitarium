package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func TestMergeRunUsesIdempotentGuardedWorkflow(t *testing.T) {
	now := time.Date(2026, time.September, 11, 17, 0, 0, 0, time.UTC)
	run := execution.Run{
		ID: "run_merge", FeatureID: "fea_test", Status: execution.RunStatusSucceeded,
		AgentProviders: project.DefaultAgentProviders(),
		MergePolicy:    project.MergePolicyRequireUserApproval,
		StartedAt:      now, UpdatedAt: now, EndedAt: &now,
	}
	workflow := &planningStarterStub{run: run}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, workflow,
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_merge/merge", nil)
	request.Header.Set("Idempotency-Key", "merge-approved-revision")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || workflow.receivedRunID != run.ID ||
		workflow.receivedKey != "merge-approved-revision" {
		t.Fatalf("merge response status=%d run=%q key=%q body=%s", recorder.Code, workflow.receivedRunID, workflow.receivedKey, recorder.Body.String())
	}
	var response runResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil ||
		response.ID != run.ID || response.Status != execution.RunStatusSucceeded {
		t.Fatalf("decode merge response: response=%+v err=%v", response, err)
	}
}

func TestMergeRunRequiresKeyAndReadyRevision(t *testing.T) {
	workflow := &planningStarterStub{err: orchestration.ErrImplementationNotAllowed}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, workflow,
	)
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_merge/merge", nil,
	))
	if missing.Code != http.StatusBadRequest || workflow.receivedRunID != "" {
		t.Fatalf("missing key status=%d workflow_run=%q", missing.Code, workflow.receivedRunID)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_merge/merge", nil)
	request.Header.Set("Idempotency-Key", "not-ready")
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, request)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("not-ready status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	var response testErrorResponse
	if err := json.NewDecoder(conflict.Body).Decode(&response); err != nil ||
		response.Error.Code != "merge_not_ready" {
		t.Fatalf("unexpected conflict response %+v err=%v", response, err)
	}
}
