package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
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
	// handling names the work orders autopilot is on.
	handling map[string]bool
	// blockedAt is the blocker time attention last asked about.
	blockedAt time.Time
	// settings are the assistant's; an empty agent means not set up.
	settings conversations.Settings
	// goal is the conversation's autopilot goal.
	goal *conversations.Goal
}

// testAgents are a Codex and a Claude agent, both connected.
func testAgents() *recordingAgentService {
	return &recordingAgentService{agents: map[string]agent.Agent{
		"codex":  {ID: "codex", Name: "Codex", Provider: project.AgentProviderCodex},
		"claude": {ID: "claude", Name: "Claude", Provider: project.AgentProviderClaude},
	}}
}

func (stub *conversationStub) AutopilotHandles(_ context.Context, _, workOrderID string, blockedAt time.Time) bool {
	stub.blockedAt = blockedAt
	return stub.handling[workOrderID]
}

func (stub *conversationStub) Settings(context.Context) (conversations.Settings, error) {
	settings := stub.settings
	settings.AskBefore = conversations.DefaultAskBefore
	return settings, nil
}

func (stub *conversationStub) SaveSettings(_ context.Context, settings conversations.Settings) (conversations.Settings, error) {
	return settings, nil
}

func (stub *conversationStub) Get(context.Context, string) (conversations.Conversation, error) {
	return conversations.Conversation{Goal: stub.goal}, nil
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
			LeadAgent: "claude", LeadModel: "claude-fable-5-1",
		},
	}}
	features := &recordingFeatureService{createResult: feature.Feature{ID: "fea_sync", ProjectID: "prj_test", Title: "Sync meals", State: feature.StateDraft}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub, testAgents())
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
	if features.receivedOverrides.CreatedBy != feature.CreatedByAssistant {
		t.Fatalf("the assistant's work order is created by %q", features.receivedOverrides.CreatedBy)
	}
	if features.receivedTitle != "Sync meals" || features.receivedOverrides.Steps == nil ||
		!features.receivedOverrides.Steps.SkipPlan || !features.receivedOverrides.Steps.SkipReviewer {
		t.Fatalf("work order = %q %+v", features.receivedTitle, features.receivedOverrides)
	}
	// The assistant's choice, with the lead named for the skipped reviewer.
	if chosen := features.receivedOverrides.AgentProviders; chosen == nil || chosen.LeadAgent != "claude" ||
		chosen.ReviewerAgent != "claude" || features.receivedOverrides.AgentModels.Reviewer != "claude-fable-5-1" {
		t.Fatalf("agents = %+v models = %+v", chosen, features.receivedOverrides.AgentModels)
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
			LeadAgent: "claude", LeadModel: "claude-fable-5-1",
		},
	}}
	features := &recordingFeatureService{createResult: feature.Feature{ID: "fea_sync", ProjectID: "prj_test", Title: "Sync meals", State: feature.StateDraft}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub, testAgents())
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

// Autopilot never acts on a work order the user created by hand, unless the
// user handed it over with a goal; the user's own approval of the same card
// may (ADR-027).
func TestAutopilotLeavesTheUsersWorkOrdersToTheUser(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantContinueWorkOrder, Reason: "The draft has been waiting", WorkOrderID: "fea_users",
		},
	}}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_users", ProjectID: "prj_test", Title: "Prevent logging future nights", CreatedBy: feature.CreatedByUser,
	}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test", App: true}}
	executions := &recordingExecutionService{runs: []execution.Run{{ID: "run_1", StartedAt: time.Now()}}}
	workflow := &planningStarterStub{run: execution.Run{ID: "run_1"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, workflow, nil, nil, nil, nil, stub)
	apply := AssistantActionApplier(handler)

	err := apply(t.Context(), "prj_test", "act_1")
	if err == nil || !strings.Contains(err.Error(), "“Prevent logging future nights” is the user's work order") ||
		!strings.Contains(err.Error(), "waiting for them") || workflow.receivedRunID != "" || stub.resolvedStatus != "" {
		t.Fatalf("autopilot acted on the user's work order: %v (run %q)", err, workflow.receivedRunID)
	}

	// The user approves the same card: it runs.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/v1/assistant/conversations/prj_test/actions/act_1/approve", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusOK || workflow.receivedRunID != "run_1" || stub.resolvedStatus != conversations.ActionApplied {
		t.Fatalf("the user's approval: %d %s", recorder.Code, recorder.Body.String())
	}

	// Handed over with a goal, it is autopilot's too.
	stub.resolvedStatus, workflow.receivedRunID = "", ""
	stub.goal = &conversations.Goal{Status: conversations.GoalActive, Handed: []string{"fea_users"}}
	if err := apply(t.Context(), "prj_test", "act_1"); err != nil || workflow.receivedRunID != "run_1" {
		t.Fatalf("a handed work order: %v (run %q)", err, workflow.receivedRunID)
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
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_log", ProjectID: "prj_test", Title: "Show the log", CreatedBy: feature.CreatedByAssistant,
	}}
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
