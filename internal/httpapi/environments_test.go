package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestApprovedEnvironmentPackagesAreReadWithoutARequest(t *testing.T) {
	environments := &attentionEnvironmentService{packages: []string{"git-lfs", "libvips-dev"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		&recordingProjectService{}, &recordingFeatureService{}, nil, &recordingExecutionService{},
		nil, nil, nil, nil, nil, nil, nil, nil, environments,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/environment/approved-packages", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	var response struct {
		ApprovedPackages []string `json:"approved_packages"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode approved packages: %v", err)
	}
	if !slices.Equal(response.ApprovedPackages, []string{"git-lfs", "libvips-dev"}) {
		t.Fatalf("unexpected approved packages: %+v", response.ApprovedPackages)
	}
}

// rejectingEnvironmentService records a rejection as the store does.
type rejectingEnvironmentService struct {
	attentionEnvironmentService
}

func (*rejectingEnvironmentService) Reject(_ context.Context, id, reason string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{
		ID: id, RunID: "run_one", SessionID: "run_one:lead", Status: projectenvironment.StatusRejected, Error: reason,
	}, true, nil
}

// resumingController stands in for the real-agent workflow, which resumes
// the lead itself rather than only sending it a reply.
type resumingController struct {
	err      error
	request  projectenvironment.Request
	message  string
	commands int
}

func (controller *resumingController) SendCommand(context.Context, string, worker.Command) (execution.Command, error) {
	controller.commands++
	return execution.Command{}, errors.New("a reply is not how the lead resumes")
}

func (controller *resumingController) ResumeAfterEnvironmentRejection(
	_ context.Context, request projectenvironment.Request, message string,
) error {
	controller.request, controller.message = request, message
	return controller.err
}

func TestRejectingAnEnvironmentRequestResumesTheLeadWithTheReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "resumed", status: http.StatusOK},
		{name: "not yet", err: errors.New("command is not allowed for session state"), status: http.StatusConflict},
	} {
		controller := &resumingController{err: tc.err}
		handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
			&recordingProjectService{}, &recordingFeatureService{}, nil, &recordingExecutionService{},
			controller, nil, nil, nil, nil, nil, nil, nil, &rejectingEnvironmentService{},
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(
			http.MethodPost, "/api/v1/environment-requests/env_one/reject",
			strings.NewReader(`{"reason":"Workers have no Docker."}`),
		))
		if recorder.Code != tc.status {
			t.Fatalf("%s: expected status %d, got %d: %s", tc.name, tc.status, recorder.Code, recorder.Body.String())
		}
		if controller.commands != 0 || controller.request.ID != "env_one" ||
			!strings.Contains(controller.message, "The user declined the requested system packages.") ||
			!strings.Contains(controller.message, "The user's reason: Workers have no Docker.") {
			t.Fatalf("%s: the lead was not resumed with the reason: %+v", tc.name, controller)
		}
	}
}
