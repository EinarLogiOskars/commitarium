package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type validationServiceStub struct {
	disabledProject string
	completed       validation.Job
}

func (stub *validationServiceStub) Configure(context.Context, string, []string) (validation.Config, error) {
	return validation.Config{}, nil
}
func (stub *validationServiceStub) Disable(_ context.Context, projectID string) (bool, error) {
	stub.disabledProject = projectID
	return true, nil
}
func (stub *validationServiceStub) GetConfig(context.Context, string) (validation.Config, error) {
	return validation.Config{}, validation.ErrNotFound
}
func (stub *validationServiceStub) GetJob(context.Context, string) (validation.Job, error) {
	return validation.Job{}, validation.ErrNotFound
}
func (stub *validationServiceStub) JobsForRun(context.Context, string) ([]validation.Job, error) {
	return nil, nil
}
func (stub *validationServiceStub) Claim(context.Context, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}
func (stub *validationServiceStub) Complete(context.Context, string, []validation.CommandResult, string) (validation.Job, bool, error) {
	return stub.completed, true, nil
}
func (stub *validationServiceStub) Retry(context.Context, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}

type failingValidationPublisher struct{ WorkspaceService }

func (failingValidationPublisher) Get(context.Context, string, string) (workspace.Workspace, error) {
	return workspace.Workspace{PullRequestNumber: 5}, nil
}

func (failingValidationPublisher) PublishValidationResult(context.Context, string, string, workspace.ValidationPublicationSpec) (bool, error) {
	return false, errors.New("Forgejo refused the comment")
}

func TestDisableProjectValidationRemovesTheMergeGate(t *testing.T) {
	validations := &validationServiceStub{}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, validations,
	)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test/validation", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || validations.disabledProject != "prj_test" {
		t.Fatalf("disable validation = status %d project %q body %s", response.Code, validations.disabledProject, response.Body.String())
	}
}

func TestCompletedValidationDoesNotFailWhenForgejoSummaryCannotPublish(t *testing.T) {
	validations := &validationServiceStub{completed: validation.Job{
		ID: "val_test", ProjectID: "prj_test", FeatureID: "fea_test", RunID: "run_test",
		CommitID: "0123456789abcdef0123456789abcdef01234567", Status: validation.StatusPassed,
		Results: []validation.CommandResult{{Command: "node --version", ExitCode: 0}},
	}}
	api := &API{validations: validations, workspaces: failingValidationPublisher{}}
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/validation-jobs/val_test/complete",
		strings.NewReader(`{"results":[{"command":"node --version","exit_code":0,"output":"v22","duration_ms":5}],"error":""}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("id", "val_test")
	response := httptest.NewRecorder()

	api.completeValidationJobHandler(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Warning") == "" {
		t.Fatalf("complete validation = status %d warning %q body %s", response.Code, response.Header().Get("Warning"), response.Body.String())
	}
}
