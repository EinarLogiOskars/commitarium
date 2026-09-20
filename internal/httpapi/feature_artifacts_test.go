package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type artifactWorkflowService struct {
	*recordingWorkflowService
	artifact         workflow.FeatureArtifact
	artifactErr      error
	putDraft         featureartifact.GoalDraft
	expectedRevision int
	planVersion      int
	stepID           string
	stepStatus       featureartifact.StepStatus
	commitID         string
	artifactActor    workflow.Actor
	artifactKey      string
}

func (s *artifactWorkflowService) GetFeatureArtifact(
	_ context.Context,
	_ string,
	_ featureartifact.Kind,
) (workflow.FeatureArtifact, error) {
	return s.artifact, s.artifactErr
}

func (s *artifactWorkflowService) PutGoalDraft(
	_ context.Context,
	_ string,
	expectedRevision int,
	draft featureartifact.GoalDraft,
	actor workflow.Actor,
	key string,
) (workflow.FeatureArtifact, error) {
	s.expectedRevision = expectedRevision
	s.putDraft = draft
	s.artifactActor = actor
	s.artifactKey = key
	return s.artifact, s.artifactErr
}

func (s *artifactWorkflowService) TransitionImplementationPlanStep(
	_ context.Context,
	_ string,
	planVersion int,
	stepID string,
	status featureartifact.StepStatus,
	commitID string,
	actor workflow.Actor,
	key string,
) (workflow.FeatureArtifact, error) {
	s.planVersion = planVersion
	s.stepID = stepID
	s.stepStatus = status
	s.commitID = commitID
	s.artifactActor = actor
	s.artifactKey = key
	return s.artifact, s.artifactErr
}

func TestGetFeatureArtifact(t *testing.T) {
	updatedAt := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	service := &artifactWorkflowService{
		recordingWorkflowService: &recordingWorkflowService{},
		artifact: workflow.FeatureArtifact{
			FeatureID: "fea_test", Kind: featureartifact.KindGoalDraft, Revision: 2,
			Document: `{"goal":"Ship it.","open_questions":[]}`,
			Actor:    workflow.Actor{Kind: workflow.ActorKindAgent, ID: "lead"}, UpdatedAt: updatedAt,
		},
	}
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}}
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/prj_test/features/fea_test/artifacts/goal_draft", nil)
	recorder := httptest.NewRecorder()

	New(nil, features, service, nil, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	response := featureArtifactResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode artifact response: %v", err)
	}
	if response.Revision != 2 || response.Kind != featureartifact.KindGoalDraft ||
		response.UpdatedBy.ID != "lead" || string(response.Document) != service.artifact.Document {
		t.Fatalf("unexpected artifact response: %+v", response)
	}
}

func TestPutGoalDraftArtifact(t *testing.T) {
	service := &artifactWorkflowService{
		recordingWorkflowService: &recordingWorkflowService{},
		artifact: workflow.FeatureArtifact{
			FeatureID: "fea_test", Kind: featureartifact.KindGoalDraft, Revision: 3,
			Document: `{"goal":"Edited goal.","open_questions":[]}`,
			Actor:    workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID}, UpdatedAt: time.Now().UTC(),
		},
	}
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}}
	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/projects/prj_test/features/fea_test/artifacts/goal_draft",
		strings.NewReader(`{"expected_revision":2,"document":{"goal":"Edited goal.","open_questions":[]}}`))
	request.Header.Set("Idempotency-Key", "edit-goal-1")
	recorder := httptest.NewRecorder()

	New(nil, features, service, nil, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if service.expectedRevision != 2 || service.putDraft.Goal != "Edited goal." ||
		service.artifactActor != (workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID}) ||
		service.artifactKey != "edit-goal-1" {
		t.Fatalf("unexpected goal update: %+v", service)
	}
}

func TestTransitionImplementationPlanStep(t *testing.T) {
	service := &artifactWorkflowService{
		recordingWorkflowService: &recordingWorkflowService{},
		artifact: workflow.FeatureArtifact{
			FeatureID: "fea_test", Kind: featureartifact.KindImplementationPlan, Revision: 4,
			Document: `{}`, Actor: workflow.Actor{Kind: workflow.ActorKindAgent, ID: "implementation-lead"},
			UpdatedAt: time.Now().UTC(),
		},
	}
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}}
	commitID := strings.Repeat("a", 40)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/prj_test/features/fea_test/implementation-plan/steps/api/transitions",
		strings.NewReader(`{"plan_version":3,"status":"completed","commit_id":"`+commitID+`"}`))
	request.Header.Set("Idempotency-Key", "complete-api")
	recorder := httptest.NewRecorder()

	New(nil, features, service, nil, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if service.planVersion != 3 || service.stepID != "api" ||
		service.stepStatus != featureartifact.StepCompleted || service.commitID != commitID ||
		service.artifactKey != "complete-api" {
		t.Fatalf("unexpected plan transition: %+v", service)
	}
}

func TestFeatureArtifactErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing", err: workflow.ErrArtifactNotFound, status: 404, code: "artifact_not_found"},
		{name: "stale revision", err: workflow.ErrArtifactConflict, status: 409, code: "artifact_revision_conflict"},
		{name: "invalid", err: featureartifact.ErrInvalidArtifact, status: 400, code: "invalid_feature_artifact"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &artifactWorkflowService{
				recordingWorkflowService: &recordingWorkflowService{}, artifactErr: test.err,
			}
			features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}}
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/projects/prj_test/features/fea_test/artifacts/goal_draft", nil)
			recorder := httptest.NewRecorder()

			New(nil, features, service, nil, nil, nil).ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d", test.status, recorder.Code)
			}
			response := errorResponse{}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if response.Error.Code != test.code {
				t.Fatalf("expected code %q, got %q", test.code, response.Error.Code)
			}
		})
	}
}
