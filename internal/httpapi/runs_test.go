package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type recordingRunStarter struct {
	runID       string
	projectID   string
	featureID   string
	goal        string
	limits      project.DialogueLimits
	providers   project.AgentProviders
	mergePolicy project.MergePolicy
	result      execution.Run
	err         error
}

func (s *recordingRunStarter) Start(
	_ context.Context,
	runID string,
	projectID string,
	featureID string,
	goal string,
	dialogueLimits project.DialogueLimits,
	agentProviders project.AgentProviders,
	mergePolicy project.MergePolicy,
) (execution.Run, bool, error) {
	s.runID = runID
	s.projectID = projectID
	s.featureID = featureID
	s.goal = goal
	s.limits = dialogueLimits
	s.providers = agentProviders
	s.mergePolicy = mergePolicy
	if s.result.ID == "" {
		s.result = execution.Run{
			ID: runID, FeatureID: featureID, Status: execution.RunStatusRunning,
			PlanningRoundLimit:             dialogueLimits.PlanningRounds,
			ImplementationReviewRoundLimit: dialogueLimits.ImplementationReviewRounds,
			AgentProviders:                 s.providers,
			MergePolicy:                    s.mergePolicy,
			StartedAt:                      time.Now().UTC(), UpdatedAt: time.Now().UTC(),
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
	limits := project.DialogueLimits{PlanningRounds: 3, ImplementationReviewRounds: 0}
	providers := project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
	}
	projects := &recordingProjectService{getByIDResult: project.Project{
		ID: "prj_test", DialogueLimits: limits, AgentProviders: providers,
		MergePolicy: project.MergePolicyAutoAfterGates,
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/prj_test/features/fea_test/runs",
		nil,
	)
	request.Header.Set("Idempotency-Key", "start-demo")
	recorder := httptest.NewRecorder()

	New(projects, features, nil, executions, nil, starter).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, recorder.Code, recorder.Body.String())
	}
	expectedRunID := runIDForKey("start-demo")
	if starter.runID != expectedRunID || starter.projectID != "prj_test" || starter.featureID != "fea_test" {
		t.Errorf(
			"unexpected start request run=%q project=%q feature=%q",
			starter.runID, starter.projectID, starter.featureID,
		)
	}
	if starter.goal != "Build coordinator: Exercise its deterministic workflow" {
		t.Errorf("unexpected derived goal %q", starter.goal)
	}
	if starter.limits != limits {
		t.Errorf("expected run snapshot %+v, got %+v", limits, starter.limits)
	}
	if starter.providers != providers {
		t.Errorf("expected provider snapshot %+v, got %+v", providers, starter.providers)
	}
	if starter.mergePolicy != project.MergePolicyAutoAfterGates {
		t.Errorf("expected merge-policy snapshot %q, got %q", project.MergePolicyAutoAfterGates, starter.mergePolicy)
	}
	if location := recorder.Header().Get("Location"); location != "/api/v1/runs/"+expectedRunID {
		t.Errorf("unexpected Location %q", location)
	}
	var body runResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if body.ID != expectedRunID || body.Status != execution.RunStatusRunning || body.Sessions == nil ||
		body.DialogueLimits.PlanningRounds != 3 || body.DialogueLimits.ImplementationReviewRounds != 0 {
		t.Errorf("unexpected run response %+v", body)
	}
	if body.AgentProviders.Lead != providers.Lead || body.AgentProviders.Reviewer != providers.Reviewer {
		t.Errorf("unexpected run provider response %+v", body.AgentProviders)
	}
	if body.MergePolicy != project.MergePolicyAutoAfterGates {
		t.Errorf("unexpected run merge policy %q", body.MergePolicy)
	}
}

func TestStartRunRetryReturnsExistingRunAfterFeatureAdvances(t *testing.T) {
	runID := runIDForKey("same-request")
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", State: feature.StateDraft,
		AcceptedGoal: "Ship the clarified goal.",
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
		{name: "goal already accepted", key: "new", feature: feature.Feature{State: feature.StateDraft, AcceptedGoal: "Ship it"}, status: http.StatusConflict, code: "feature_not_startable"},
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

func TestStartRunMapsSelectedProjectWorkspaceErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "unbound repository", err: workspace.ErrProjectRepositoryNotBound, status: http.StatusConflict, code: "forgejo_repository_not_bound"},
		{name: "repository not ready", err: project.ErrForgejoRepositoryNotReady, status: http.StatusConflict, code: "forgejo_repository_not_ready"},
		{name: "checkout conflict", err: workspace.ErrCheckoutConflict, status: http.StatusConflict, code: "workspace_conflict"},
		{name: "checkout unavailable", err: workspace.ErrCheckoutUnavailable, status: http.StatusServiceUnavailable, code: "checkout_unavailable"},
		{name: "Forgejo unavailable", err: project.ErrForgejoUnavailable, status: http.StatusServiceUnavailable, code: "forgejo_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			features := &recordingFeatureService{getResult: feature.Feature{
				ID: "fea_test", ProjectID: "prj_test", Title: "Test", State: feature.StateDraft,
			}}
			executions := &recordingExecutionService{runErr: execution.ErrNotFound}
			projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
			starter := &recordingRunStarter{err: test.err}
			request := httptest.NewRequest(
				http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/runs", nil,
			)
			request.Header.Set("Idempotency-Key", "start-with-workspace")
			recorder := httptest.NewRecorder()

			New(projects, features, nil, executions, nil, starter).ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d: %s", test.status, recorder.Code, recorder.Body.String())
			}
			var body errorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if body.Error.Code != test.code {
				t.Fatalf("expected code %q, got %+v", test.code, body)
			}
		})
	}
}

func TestGetRunIncludesSessions(t *testing.T) {
	now := time.Date(2026, time.September, 9, 3, 0, 0, 0, time.UTC)
	executions := &recordingExecutionService{
		run: execution.Run{
			ID: "run_test", FeatureID: "fea_test", Status: execution.RunStatusRunning,
			PlanningRoundLimit: 6, ImplementationReviewRoundLimit: 4,
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
	if body.ID != "run_test" || len(body.Sessions) != 1 || body.Sessions[0].Role != worker.RoleCoder ||
		body.DialogueLimits.PlanningRounds != 6 || body.DialogueLimits.ImplementationReviewRounds != 4 {
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

func TestListFeatureRunsIncludesSessions(t *testing.T) {
	now := time.Date(2026, time.September, 10, 13, 0, 0, 0, time.UTC)
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test",
	}}
	executions := &recordingExecutionService{
		runs: []execution.Run{{
			ID: "run_test", FeatureID: "fea_test", Status: execution.RunStatusWaitingForUser,
			PlanningRoundLimit: 6, ImplementationReviewRoundLimit: 6,
			StartedAt: now, UpdatedAt: now,
		}},
		sessions: []execution.Session{{
			ID: "ses_lead", RunID: "run_test", AgentID: "agt_codex",
			Role: worker.RoleCoder, Status: execution.SessionStatusWaitingForUser,
			StartedAt: now, UpdatedAt: now,
		}},
	}
	recorder := httptest.NewRecorder()
	New(nil, features, nil, executions, nil, nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/features/fea_test/runs", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if executions.runsFeature != "fea_test" {
		t.Errorf("expected run lookup for fea_test, got %q", executions.runsFeature)
	}
	var body []runResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode run list: %v", err)
	}
	if len(body) != 1 || body[0].ID != "run_test" || len(body[0].Sessions) != 1 || body[0].Sessions[0].ID != "ses_lead" {
		t.Errorf("unexpected run history %+v", body)
	}
}

func TestListFeatureRunsReturnsEmptyArray(t *testing.T) {
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test",
	}}
	recorder := httptest.NewRecorder()
	New(nil, features, nil, &recordingExecutionService{}, nil, nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/features/fea_test/runs", nil),
	)
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Errorf("expected 200 with empty array, got %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestListFeatureRunsMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		featureErr error
		runsErr    error
		status     int
		code       string
	}{
		{name: "unknown feature", featureErr: feature.ErrNotFound, status: http.StatusNotFound, code: "feature_not_found"},
		{name: "feature lookup failure", featureErr: errors.New("feature database failed"), status: http.StatusInternalServerError, code: "internal_error"},
		{name: "run lookup failure", runsErr: errors.New("run database failed"), status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			features := &recordingFeatureService{
				getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"},
				getErr:    test.featureErr,
			}
			executions := &recordingExecutionService{runsErr: test.runsErr}
			recorder := httptest.NewRecorder()
			New(nil, features, nil, executions, nil, nil).ServeHTTP(
				recorder,
				httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/features/fea_test/runs", nil),
			)
			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d", test.status, recorder.Code)
			}
			var body errorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != test.code {
				t.Errorf("expected code %q, got %+v", test.code, body)
			}
		})
	}
}
