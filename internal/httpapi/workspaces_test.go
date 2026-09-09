package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type recordingWorkspaceService struct {
	projectID string
	featureID string
	result    workspace.Workspace
	created   bool
	err       error
}

func (service *recordingWorkspaceService) Get(
	_ context.Context,
	projectID string,
	featureID string,
) (workspace.Workspace, error) {
	service.projectID = projectID
	service.featureID = featureID
	return service.result, service.err
}

func (service *recordingWorkspaceService) Prepare(
	_ context.Context,
	projectID string,
	featureID string,
) (workspace.Workspace, bool, error) {
	service.projectID = projectID
	service.featureID = featureID
	return service.result, service.created, service.err
}

func TestPrepareWorkspaceCreatesBranchReservation(t *testing.T) {
	stored := testWorkspaceResponseValue()
	service := &recordingWorkspaceService{result: stored, created: true}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/projects/prj_test/features/fea_test/workspace",
		nil,
	)
	recorder := httptest.NewRecorder()
	NewWithWorkspaceService(nil, nil, nil, nil, nil, nil, service).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("expected %d, got %d", http.StatusCreated, response.StatusCode)
	}
	if response.Header.Get("Location") != request.URL.Path {
		t.Fatalf("unexpected Location %q", response.Header.Get("Location"))
	}
	if service.projectID != "prj_test" || service.featureID != "fea_test" {
		t.Fatalf("unexpected service arguments %q %q", service.projectID, service.featureID)
	}
	var body workspaceResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if body.ID != stored.ID || body.Branch != stored.Branch ||
		body.BaseCommitID != stored.BaseCommitID || body.Status != workspace.StatusBranchReady ||
		body.Checkout == nil || body.Checkout.RelativePath != stored.CheckoutRelativePath ||
		body.PullRequest == nil || body.PullRequest.Number != stored.PullRequestNumber ||
		body.PullRequest.URL != stored.PullRequestURL || !body.PullRequest.Draft ||
		!body.PullRequest.RecordedAt.Equal(*stored.PullRequestRecordedAt) {
		t.Fatalf("unexpected workspace response %+v", body)
	}
}

func TestPrepareWorkspaceRetryReturnsOK(t *testing.T) {
	service := &recordingWorkspaceService{result: testWorkspaceResponseValue()}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/projects/prj_test/features/fea_test/workspace",
		nil,
	)
	recorder := httptest.NewRecorder()
	NewWithWorkspaceService(nil, nil, nil, nil, nil, nil, service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, recorder.Code)
	}
}

func TestGetWorkspace(t *testing.T) {
	service := &recordingWorkspaceService{result: testWorkspaceResponseValue()}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test/features/fea_test/workspace",
		nil,
	)
	recorder := httptest.NewRecorder()
	NewWithWorkspaceService(nil, nil, nil, nil, nil, nil, service).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, recorder.Code)
	}
}

func TestPrepareWorkspaceMapsSafeErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing feature", err: feature.ErrNotFound, status: http.StatusNotFound, code: "feature_not_found"},
		{name: "unaccepted goal", err: workspace.ErrGoalNotAccepted, status: http.StatusConflict, code: "goal_not_accepted"},
		{name: "feature already advanced", err: workspace.ErrFeatureNotDraft, status: http.StatusConflict, code: "workspace_preparation_not_allowed"},
		{name: "unbound repository", err: workspace.ErrProjectRepositoryNotBound, status: http.StatusConflict, code: "forgejo_repository_not_bound"},
		{name: "branch conflict", err: workspace.ErrBranchConflict, status: http.StatusConflict, code: "workspace_conflict"},
		{name: "checkout conflict", err: workspace.ErrCheckoutConflict, status: http.StatusConflict, code: "workspace_conflict"},
		{name: "pull request conflict", err: workspace.ErrPullRequestConflict, status: http.StatusConflict, code: "workspace_conflict"},
		{name: "checkout unavailable", err: workspace.ErrCheckoutUnavailable, status: http.StatusServiceUnavailable, code: "checkout_unavailable"},
		{name: "Forgejo unavailable", err: project.ErrForgejoUnavailable, status: http.StatusServiceUnavailable, code: "forgejo_unavailable"},
		{name: "internal", err: errors.New("boom"), status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingWorkspaceService{err: test.err}
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/v1/projects/prj_test/features/fea_test/workspace",
				nil,
			)
			recorder := httptest.NewRecorder()
			NewWithWorkspaceService(nil, nil, nil, nil, nil, nil, service).ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("expected %d, got %d", test.status, recorder.Code)
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

func testWorkspaceResponseValue() workspace.Workspace {
	createdAt := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	readyAt := createdAt.Add(time.Second)
	checkoutAt := readyAt.Add(time.Second)
	pullRequestAt := checkoutAt.Add(time.Second)
	return workspace.Workspace{
		ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
		RepositoryOwner: "owner", RepositoryName: "repository",
		BaseBranch: "main", Branch: "commitarium/fea_test",
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
		Status:       workspace.StatusBranchReady, BranchCreatedAt: &readyAt,
		CheckoutRelativePath: "wsp_fea_test", CheckoutCreatedAt: &checkoutAt,
		PullRequestNumber:     7,
		PullRequestURL:        "http://localhost:3001/owner/repository/pulls/7",
		PullRequestRecordedAt: &pullRequestAt,
		CreatedAt:             createdAt, UpdatedAt: pullRequestAt,
	}
}
