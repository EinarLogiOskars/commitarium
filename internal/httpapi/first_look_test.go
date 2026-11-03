package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type firstLookConversations struct {
	*conversationStub
	scope    string
	settings []string
	builds   bool
}

func (stub *firstLookConversations) FirstLook(_ context.Context, scope string, settings []string, builds bool) error {
	stub.scope, stub.settings, stub.builds = scope, settings, builds
	return nil
}

// The first look hands the assistant the project's settings in plain words.
func TestAFirstLookTellsTheAssistantTheProjectsSettings(t *testing.T) {
	stub := &firstLookConversations{conversationStub: &conversationStub{}}
	projects := &recordingProjectService{getByIDResult: project.Project{
		ID: "prj_test", MergePolicy: project.MergePolicyRequireUserApproval,
		AutonomyPolicy: project.AutonomyPolicyReviewEachPhase, RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		DialogueLimits: project.DialogueLimits{PlanningRounds: 10, ImplementationReviewRounds: 0},
	}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, stub)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/first-look", nil))
	if response.Code != http.StatusAccepted || stub.scope != "prj_test" || stub.builds {
		t.Fatalf("status %d, scope %q, builds %t", response.Code, stub.scope, stub.builds)
	}
	want := []string{
		"Each work order stops for the user's review after its plan and after its implementation.",
		"Nothing merges into main until the user approves it.",
		"The agents may take 10 rounds on a plan and any number of rounds of code review before they stop for the user.",
		"A run that fails waits for the user to retry or change it.",
	}
	if !slices.Equal(stub.settings, want) {
		t.Fatalf("settings = %q", stub.settings)
	}
}
