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
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type recordingRunStarter struct {
	runID     string
	featureID string
	goal      string
	result    execution.Run
	err       error
}

func (s *recordingRunStarter) Start(
	_ context.Context,
	runID string,
	featureID string,
	goal string,
) (execution.Run, bool, error) {
	s.runID = runID
	s.featureID = featureID
	s.goal = goal
	if s.result.ID == "" {
		s.result = execution.Run{
			ID: runID, FeatureID: featureID, Status: execution.RunStatusRunning,
			StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	return s.result, true, s.err
}

func TestStartRunReturnsDurableAcceptedRun(t *testing.T) {
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Build coordinator",
		Description: "Exercise its deterministic workflow", State: feature.StateDraft,
	}}
	executions := &recordingExecutionService{runErr: execution.ErrNotFound}
	starter := &recordingRunStarter{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/prj_test/features/fea_test/runs",
		nil,
	)
	request.Header.Set("Idempotency-Key", "start-demo")
	recorder := httptest.NewRecorder()

	New(nil, features, nil, executions, nil, starter).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, recorder.Code, recorder.Body.String())
	}
	expectedRunID := runIDForKey("start-demo")
	if starter.runID != expectedRunID || starter.featureID != "fea_test" {
		t.Errorf("unexpected start request run=%q feature=%q", starter.runID, starter.featureID)
	}
	if starter.goal != "Build coordinator: Exercise its deterministic workflow" {
		t.Errorf("unexpected derived goal %q", starter.goal)
	}
	if location := recorder.Header().Get("Location"); location != "/api/v1/runs/"+expectedRunID {
		t.Errorf("unexpected Location %q", location)
	}
	var body runResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if body.ID != expectedRunID || body.Status != execution.RunStatusRunning || body.Sessions == nil {
		t.Errorf("unexpected run response %+v", body)
	}
}

func TestStartRunRetryReturnsExistingRunAfterFeatureAdvances(t *testing.T) {
	runID := runIDForKey("same-request")
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", State: feature.StateReviewing,
	}}
	executions := &recordingExecutionService{
		run: execution.Run{ID: runID, FeatureID: "fea_test", Status: execution.RunStatusRunning},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/prj_test/features/fea_test/runs",
		nil,
	)
	request.Header.Set("Idempotency-Key", "same-request")
	recorder := httptest.NewRecorder()

	New(nil, features, nil, executions, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected retry status %d, got %d: %s", http.StatusAccepted, recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/api/v1/runs/"+runID {
		t.Errorf("unexpected retry Location %q", location)
	}
}

func TestStartRunValidatesAdmission(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		feature    feature.Feature
		featureErr error
		status     int
		code       string
	}{
		{name: "missing key", feature: feature.Feature{State: feature.StateDraft}, status: http.StatusBadRequest, code: "idempotency_key_required"},
		{name: "unknown feature", key: "new", featureErr: feature.ErrNotFound, status: http.StatusNotFound, code: "feature_not_found"},
		{name: "feature already active", key: "new", feature: feature.Feature{State: feature.StatePlanning}, status: http.StatusConflict, code: "feature_not_startable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			features := &recordingFeatureService{getResult: test.feature, getErr: test.featureErr}
			executions := &recordingExecutionService{runErr: execution.ErrNotFound}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/runs", nil)
			request.Header.Set("Idempotency-Key", test.key)
			recorder := httptest.NewRecorder()
			New(nil, features, nil, executions, nil, &recordingRunStarter{}).ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d", test.status, recorder.Code)
			}
			var body errorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if body.Error.Code != test.code {
				t.Errorf("expected code %q, got %+v", test.code, body)
			}
		})
	}
}

func TestGetRunIncludesSessions(t *testing.T) {
	now := time.Date(2026, time.September, 9, 3, 0, 0, 0, time.UTC)
	executions := &recordingExecutionService{
		run: execution.Run{
			ID: "run_test", FeatureID: "fea_test", Status: execution.RunStatusRunning,
			StartedAt: now, UpdatedAt: now,
		},
		sessions: []execution.Session{{
			ID: "run_test:implementation", RunID: "run_test", AgentID: "agt_fake_codex",
			Role: worker.RoleCoder, Status: execution.SessionStatusRunning,
			StartedAt: now, UpdatedAt: now,
		}},
	}
	recorder := httptest.NewRecorder()
	New(nil, nil, nil, executions, nil, nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_test", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	var body runResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if body.ID != "run_test" || len(body.Sessions) != 1 || body.Sessions[0].Role != worker.RoleCoder {
		t.Errorf("unexpected run response %+v", body)
	}
}

func TestGetRunReturnsNotFound(t *testing.T) {
	executions := &recordingExecutionService{runErr: execution.ErrNotFound}
	recorder := httptest.NewRecorder()
	New(nil, nil, nil, executions, nil, nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_missing", nil),
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
	var body errorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !errors.Is(executions.runErr, execution.ErrNotFound) || body.Error.Code != "run_not_found" {
		t.Errorf("unexpected error response %+v", body)
	}
}
