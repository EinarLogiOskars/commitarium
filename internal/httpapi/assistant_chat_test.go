package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type conversationStub struct {
	action         conversations.Action
	resolvedStatus conversations.ActionStatus
	resolvedResult string
	goalLevel      int
}

func (stub *conversationStub) Settings(context.Context) (conversations.Settings, error) {
	return conversations.Settings{AskBefore: conversations.DefaultAskBefore}, nil
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

func (stub *conversationStub) ActiveGoals(context.Context) (map[string]conversations.Goal, error) {
	return map[string]conversations.Goal{}, nil
}

func (stub *conversationStub) StartLaunch(_ context.Context, scope string) (conversations.Conversation, error) {
	return conversations.Conversation{Scope: scope}, nil
}

func (stub *conversationStub) ActiveLaunches(context.Context) (map[string]conversations.LaunchStage, error) {
	return map[string]conversations.LaunchStage{}, nil
}

func (stub *conversationStub) StopGoal(_ context.Context, scope string) (conversations.Conversation, error) {
	return conversations.Conversation{Scope: scope}, nil
}

func (stub *conversationStub) Resolve(_ context.Context, _ string, _ string, status conversations.ActionStatus, result string, _ conversations.Created) (conversations.Conversation, error) {
	stub.resolvedStatus, stub.resolvedResult = status, result
	return conversations.Conversation{}, nil
}

func TestApprovingAnAssistantActionRunsItAndPremiumNeedsConfirmation(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed, Premium: []string{"claude-fable-5-1"},
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Tricky", Title: "Sync meals", Steps: "quick",
			LeadModel: "claude-fable-5-1",
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
			LeadModel: "claude-fable-5-1",
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

func TestContinuingAWorkOrderRecoversItsNewestRunWithGuidance(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantContinueWorkOrder, Reason: "The audit comment is missing",
			WorkOrderID: "fea_log", Guidance: "Post the audit comment, then finish.",
		},
	}}
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_log", ProjectID: "prj_test", Title: "Show the log"}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	now := time.Now()
	executions := &recordingExecutionService{runs: []execution.Run{
		{ID: "run_old", StartedAt: now.Add(-time.Hour)}, {ID: "run_new", StartedAt: now},
	}}
	workflow := &planningStarterStub{run: execution.Run{ID: "run_new"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, workflow, nil, nil, nil, nil, stub)

	if err := AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1"); err != nil {
		t.Fatal(err)
	}
	if workflow.receivedRunID != "run_new" || workflow.receivedRecovery.Action != orchestration.RecoveryActionContinue ||
		workflow.receivedRecovery.Message != "Post the audit comment, then finish." {
		t.Fatalf("recovered %q with %+v", workflow.receivedRunID, workflow.receivedRecovery)
	}
	if stub.resolvedStatus != conversations.ActionApplied || !strings.Contains(stub.resolvedResult, "with guidance") {
		t.Fatalf("resolved = %s %q", stub.resolvedStatus, stub.resolvedResult)
	}

	workflow.err = orchestration.ErrRecoveryNotAllowed
	stub.action.Status = conversations.ActionProposed
	if err := AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1"); err == nil ||
		!strings.Contains(err.Error(), "not waiting on a blocker") {
		t.Fatalf("continuing a run that is not blocked = %v", err)
	}
}

func TestStartingAWorkOrderJudgesPremiumOnTheModelsThatWillRun(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantStartWorkOrder, Reason: "Next", WorkOrderID: "fea_edit",
		},
	}}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_edit", ProjectID: "prj_test", Title: "Correct entries", State: feature.StateDraft,
		AgentModels: project.AgentModels{Lead: "gpt-5.6-sol", Reviewer: "claude-fable-5-1"},
	}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)
	approve := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/assistant/conversations/prj_test/actions/act_1/approve", strings.NewReader(body)))
		return recorder
	}

	if recorder := approve(`{"start":true}`); recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "claude-fable-5-1") {
		t.Fatalf("a start on the work order's premium reviewer was not held: %d %s", recorder.Code, recorder.Body.String())
	}
	// Choosing another reviewer needs no premium approval; the start itself
	// then fails in this stub, past the premium check.
	if recorder := approve(`{"start":true,"reviewer_model":"claude-opus-5-5"}`); strings.Contains(recorder.Body.String(), "premium_approval_required") {
		t.Fatalf("the user's non-premium choice was still held: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestTheTopLevelCreatesAppsAndProjectsDoTheRest(t *testing.T) {
	for name, test := range map[string]struct {
		scope  string
		action featureartifact.AssistantActionType
	}{
		"a work order at the top level": {"commitarium", featureartifact.AssistantCreateWorkOrder},
		"an app inside a project":       {"prj_test", featureartifact.AssistantCreateApp},
	} {
		stub := &conversationStub{action: conversations.Action{
			ID: "act_1", Status: conversations.ActionProposed,
			AssistantAction: featureartifact.AssistantAction{
				Type: test.action, Reason: "Asked", Title: "Tips", Description: "A tip calculator.", Steps: "quick",
			},
		}}
		projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
		handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/assistant/conversations/"+test.scope+"/actions/act_1/approve", strings.NewReader(`{}`)))
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "action_needs_project") {
			t.Errorf("%s: %d %s", name, recorder.Code, recorder.Body.String())
		}
	}
}
