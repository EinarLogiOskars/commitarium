package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type conversationStub struct {
	action         conversations.Action
	resolvedStatus conversations.ActionStatus
	resolvedResult string
	goalLevel      int
}

func (stub *conversationStub) Settings(context.Context) (conversations.Settings, error) {
	return conversations.Settings{}, nil
}

func (stub *conversationStub) SaveSettings(_ context.Context, settings conversations.Settings) (conversations.Settings, error) {
	return settings, nil
}

func (stub *conversationStub) Get(context.Context, string) (conversations.Conversation, error) {
	return conversations.Conversation{}, nil
}

func (stub *conversationStub) Send(context.Context, string, string, string) (conversations.Conversation, error) {
	return conversations.Conversation{}, nil
}

func (stub *conversationStub) SaveNotes(context.Context, string, string) (conversations.Conversation, error) {
	return conversations.Conversation{}, nil
}

func (stub *conversationStub) Action(context.Context, string, string) (conversations.Action, error) {
	return stub.action, nil
}

func (stub *conversationStub) StartGoal(_ context.Context, scope string, level int) (conversations.Conversation, error) {
	stub.goalLevel = level
	return conversations.Conversation{Scope: scope}, nil
}

func (stub *conversationStub) StopGoal(_ context.Context, scope string) (conversations.Conversation, error) {
	return conversations.Conversation{Scope: scope}, nil
}

func (stub *conversationStub) Resolve(_ context.Context, _ string, _ string, status conversations.ActionStatus, result, _ string) (conversations.Conversation, error) {
	stub.resolvedStatus, stub.resolvedResult = status, result
	return conversations.Conversation{}, nil
}

func TestApprovingAnAssistantActionRunsItAndPremiumNeedsConfirmation(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed, Premium: []string{"claude-fable-5-1"},
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Tricky", Title: "Sync meals", Steps: "quick",
		},
	}}
	features := &recordingFeatureService{createResult: feature.Feature{ID: "fea_sync", ProjectID: "prj_test", Title: "Sync meals", State: feature.StateDraft}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)
	approve := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/assistant/conversations/prj_test/actions/act_1/approve", strings.NewReader(body)))
		return recorder
	}

	if recorder := approve(`{}`); recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "premium_approval_required") || features.createCalls != 0 {
		t.Fatalf("a premium action ran without confirmation: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder := approve(`{"confirm_premium":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if features.receivedTitle != "Sync meals" || features.receivedOverrides.Steps == nil ||
		!features.receivedOverrides.Steps.SkipPlan || !features.receivedOverrides.Steps.SkipReviewer {
		t.Fatalf("work order = %q %+v", features.receivedTitle, features.receivedOverrides)
	}
	if stub.resolvedStatus != conversations.ActionApplied || !strings.Contains(stub.resolvedResult, "Sync meals") {
		t.Fatalf("resolved = %s %q", stub.resolvedStatus, stub.resolvedResult)
	}
}

func TestAutopilotAppliesActionsThroughTheSameApproval(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed, Premium: []string{"claude-fable-5-1"},
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Tricky", Title: "Sync meals", Steps: "quick",
		},
	}}
	features := &recordingFeatureService{createResult: feature.Feature{ID: "fea_sync", ProjectID: "prj_test", Title: "Sync meals", State: feature.StateDraft}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)
	apply := AssistantActionApplier(handler)

	if err := apply(t.Context(), "prj_test", "act_1"); err == nil || !strings.Contains(err.Error(), "explicit approval") ||
		features.createCalls != 0 {
		t.Fatalf("autopilot got past the premium approval: %v", err)
	}

	stub.action = conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantStartAutopilot, Reason: "Finish the core", Priority: 1,
		},
	}
	if err := apply(t.Context(), "prj_test", "act_1"); err != nil {
		t.Fatal(err)
	}
	if stub.goalLevel != 1 || stub.resolvedStatus != conversations.ActionApplied || !strings.Contains(stub.resolvedResult, "P1") {
		t.Fatalf("goal level %d, resolved %s %q", stub.goalLevel, stub.resolvedStatus, stub.resolvedResult)
	}
}
