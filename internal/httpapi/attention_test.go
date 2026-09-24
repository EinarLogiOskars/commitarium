package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
)

type attentionEnvironmentService struct {
	requests []projectenvironment.Request
}

func (service *attentionEnvironmentService) Get(context.Context, string) (projectenvironment.Request, error) {
	return projectenvironment.Request{}, projectenvironment.ErrNotFound
}
func (service *attentionEnvironmentService) ListByProject(context.Context, string) ([]projectenvironment.Request, error) {
	return service.requests, nil
}
func (service *attentionEnvironmentService) Approve(context.Context, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) Reject(context.Context, string, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) BeginProvisioning(context.Context, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) Complete(context.Context, string, map[string]string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) Fail(context.Context, string, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) ApprovedPackages(context.Context) ([]string, error) {
	return nil, nil
}

type attentionValidationService struct {
	jobs []validation.Job
}

func (service *attentionValidationService) Configure(context.Context, string, []string) (validation.Config, error) {
	return validation.Config{}, nil
}
func (service *attentionValidationService) GetConfig(context.Context, string) (validation.Config, error) {
	return validation.Config{}, nil
}
func (service *attentionValidationService) GetJob(context.Context, string) (validation.Job, error) {
	return validation.Job{}, validation.ErrNotFound
}
func (service *attentionValidationService) JobsForRun(context.Context, string) ([]validation.Job, error) {
	return service.jobs, nil
}
func (service *attentionValidationService) Claim(context.Context, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}
func (service *attentionValidationService) Complete(context.Context, string, []validation.CommandResult, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}
func (service *attentionValidationService) Retry(context.Context, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}

func TestAttentionAggregatesProjectsAndPrefersEnvironmentRequest(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindBlocker, Reason: "missing jq", StartedAt: now, UpdatedAt: now,
	}}}
	environments := &attentionEnvironmentService{requests: []projectenvironment.Request{{
		ID: "env_one", ProjectID: "prj_one", FeatureID: "fea_one", RunID: "run_one",
		Status: projectenvironment.StatusRequested, Reason: "Install jq", UpdatedAt: now.Add(time.Minute),
	}}}

	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, environments,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "environment_approval" ||
		response.Items[0].EnvironmentRequestID != "env_one" || response.Items[0].FeatureTitle != "Ship it" {
		t.Fatalf("unexpected attention response: %+v", response)
	}
}

func TestAttentionSurfacesFailedValidation(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateReadyToMerge,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindMergeGate, StartedAt: now, UpdatedAt: now,
	}}}
	validations := &attentionValidationService{jobs: []validation.Job{{
		ID: "val_one", RunID: "run_one", Status: validation.StatusFailed,
		Error: "tests failed", CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}}}

	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, validations,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "validation_failed" ||
		response.Items[0].ValidationJobID != "val_one" || response.Items[0].Detail != "tests failed" {
		t.Fatalf("unexpected attention response: %+v", response)
	}
}

func TestAttentionRotatesIDWhenEnvironmentRetryFailsAgain(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindBlocker, StartedAt: now, UpdatedAt: now,
	}}}
	environments := &attentionEnvironmentService{requests: []projectenvironment.Request{{
		ID: "env_one", ProjectID: "prj_one", FeatureID: "fea_one", RunID: "run_one",
		Status: projectenvironment.StatusFailed, Error: "first failure", UpdatedAt: now.Add(time.Minute),
	}}}
	api := &API{projects: projects, features: features, execution: executions, environments: environments}

	first, _, err := api.collectAttention(t.Context())
	if err != nil || len(first) != 1 {
		t.Fatalf("collect first failure: items=%+v err=%v", first, err)
	}
	environments.requests[0].Error = "second failure"
	environments.requests[0].UpdatedAt = now.Add(2 * time.Minute)
	second, _, err := api.collectAttention(t.Context())
	if err != nil || len(second) != 1 {
		t.Fatalf("collect second failure: items=%+v err=%v", second, err)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("environment retry reused notification ID %q", first[0].ID)
	}
}

func TestAttentionReportsRunningWorkWithoutInboxItem(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
		}}}, nil,
		&recordingExecutionService{runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusRunning,
			StartedAt: now, UpdatedAt: now.Add(time.Minute),
		}}}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if response.Items == nil || len(response.Items) != 0 || len(response.Running) != 1 {
		t.Fatalf("expected one running work order and no items, got %+v", response)
	}
	if response.Running[0].RunID != "run_one" || response.Running[0].ProjectName != "One" ||
		response.Running[0].FeatureTitle != "Ship it" {
		t.Fatalf("unexpected running work order %+v", response.Running[0])
	}
}

func TestAttentionReportsWaitingRunOnlyAsInboxItem(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
		}}}, nil,
		&recordingExecutionService{runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
			WaitKind: execution.RunWaitKindClarification, StartedAt: now, UpdatedAt: now,
		}}}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Running == nil || len(response.Running) != 0 ||
		response.Items[0].Kind != "clarification" {
		t.Fatalf("expected one waiting item and no running work orders, got %+v", response)
	}
}

func TestAttentionIncludesBoundedAutomaticMergeHistory(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	completed := make([]feature.Feature, 30)
	for index := range completed {
		completed[index] = feature.Feature{
			ID: "fea_" + string(rune('a'+index)), ProjectID: "prj_one", Title: "Done",
			State: feature.StateCompleted, MergePolicy: project.MergePolicyAutoAfterGates,
			UpdatedAt: now.Add(time.Duration(index) * time.Minute),
		}
	}
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: completed}, nil, &recordingExecutionService{}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 25 || response.Items[0].UpdatedAt != completed[29].UpdatedAt {
		t.Fatalf("expected newest 25 automatic merges, got %+v", response.Items)
	}
}
