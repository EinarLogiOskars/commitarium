package httpapi

import (
	"context"
	"errors"
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
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// waitingExecutions are runs that wait on an agent's login or usage limit.
type waitingExecutions struct {
	*recordingExecutionService
	loginWaits map[string][]string
	limitWaits []execution.LimitWait
}

func (s *waitingExecutions) RunsAwaitingLogin(_ context.Context, agentID string) ([]string, error) {
	return s.loginWaits[agentID], nil
}

func (s *waitingExecutions) LimitWaits(context.Context) ([]execution.LimitWait, error) {
	return s.limitWaits, nil
}

func (s *waitingExecutions) LimitWaitForRun(context.Context, string) (execution.LimitWait, error) {
	return execution.LimitWait{}, execution.ErrNotFound
}

// catalogStub offers each provider's models for both roles.
type catalogStub map[project.AgentProvider][]string

func (stub catalogStub) List(context.Context) []modelcatalog.Catalog {
	fetched := time.Now()
	catalogs := make([]modelcatalog.Catalog, 0)
	for provider, ids := range stub {
		models := make([]workerhttp.Model, 0, len(ids))
		for _, id := range ids {
			models = append(models, workerhttp.Model{ID: id, DisplayName: id})
		}
		for _, role := range []modelcatalog.Role{modelcatalog.RoleLead, modelcatalog.RoleReviewer} {
			catalogs = append(catalogs, modelcatalog.Catalog{Provider: provider, Role: role, Models: models, FetchedAt: &fetched})
		}
	}
	return catalogs
}

func (stub catalogStub) Refresh(ctx context.Context) ([]modelcatalog.Catalog, error) {
	return stub.List(ctx), nil
}

func (catalogStub) ValidateSelection(context.Context, project.AgentProviders, project.AgentModels) error {
	return nil
}

var (
	codexAndClaude = project.AgentProviders{
		Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude, LeadAgent: "codex", ReviewerAgent: "claude",
	}
	offeredModels = catalogStub{
		project.AgentProviderCodex:  {"gpt-6-luna"},
		project.AgentProviderClaude: {"claude-sonnet-5-5"},
	}
)

// The coordinator never picks agents; it refuses a choice that cannot run,
// with a reason the assistant or the user can act on (ADR-027).
func TestTheCoordinatorRefusesAChoiceThatCannotRun(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	used := 40.0
	connected := stubAgentUsage{
		{Agent: "codex", ObservedAt: &now, Windows: []workerhttp.UsageWindow{{Name: "5h", UsedPercent: &used}}},
		{Agent: "claude", ObservedAt: &now},
	}
	models := project.AgentModels{Lead: "gpt-6-luna", Reviewer: "claude-sonnet-5-5"}
	for name, test := range map[string]struct {
		providers project.AgentProviders
		models    project.AgentModels
		usage     stubAgentUsage
		logins    map[string][]string
		limits    []execution.LimitWait
		skip      bool
		code      string
	}{
		"a choice that can run":   {providers: codexAndClaude, models: models, usage: connected},
		"no agents chosen":        {models: models, usage: connected, code: "agents_not_chosen"},
		"no models chosen":        {providers: codexAndClaude, usage: connected, code: "agents_not_chosen"},
		"an agent that is gone":   {providers: project.AgentProviders{Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude, LeadAgent: "codex-old", ReviewerAgent: "claude"}, models: models, usage: connected, code: "agent_not_found"},
		"a signed-out agent":      {providers: codexAndClaude, models: models, usage: connected, logins: map[string][]string{"claude": {"run_1"}}, code: "agent_signed_out"},
		"an unanswering worker":   {providers: codexAndClaude, models: models, usage: connected[:1]},
		"a run waits on a limit":  {providers: codexAndClaude, models: models, usage: connected, limits: []execution.LimitWait{{AgentID: "codex", ResetsAt: &later}}, code: "agent_at_limit"},
		"a reset limit wait":      {providers: codexAndClaude, models: models, usage: connected, limits: []execution.LimitWait{{AgentID: "codex", ResetsAt: &now}}},
		"a limited usage window":  {providers: codexAndClaude, models: models, usage: stubAgentUsage{connected[0], {Agent: "claude", Windows: []workerhttp.UsageWindow{{Name: "weekly", Limited: true, ResetsAt: &later}}}}, code: "agent_at_limit"},
		"a model it lacks":        {providers: codexAndClaude, models: project.AgentModels{Lead: "claude-sonnet-5-5", Reviewer: "claude-sonnet-5-5"}, usage: connected, code: "model_unavailable"},
		"a skipped reviewer only": {providers: codexAndClaude, models: project.AgentModels{Lead: "gpt-6-luna", Reviewer: "gpt-6-luna"}, usage: connected[:1], skip: true},
	} {
		t.Run(name, func(t *testing.T) {
			api := &API{
				agents: testAgents(), agentUsage: test.usage, modelCatalog: offeredModels,
				execution: &waitingExecutions{recordingExecutionService: &recordingExecutionService{}, loginWaits: test.logins, limitWaits: test.limits},
				now:       func() time.Time { return now },
			}
			err := api.checkWorkChoice(t.Context(), test.providers, test.models, test.skip)
			var refused *choiceError
			switch {
			case test.code == "" && err != nil:
				t.Fatalf("a choice that can run was refused: %v", err)
			case test.code != "" && (!errors.As(err, &refused) || refused.code != test.code):
				t.Fatalf("err = %v, want %s", err, test.code)
			case test.code != "" && !strings.Contains(refused.message, "choose") && !strings.Contains(refused.message, "name"):
				t.Fatalf("the reason does not say what to do: %q", refused.message)
			}
		})
	}
}

// A Start without agents is refused before anything changes: a draft stays a
// draft.
func TestAStartWithoutAgentsLeavesTheDraftAlone(t *testing.T) {
	workflowService := &artifactWorkflowService{recordingWorkflowService: &recordingWorkflowService{}}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Update the README", State: feature.StateDraft,
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/runs",
		strings.NewReader(`{"steps":{"clarification":false,"documented_plan":false,"reviewer":false}}`))
	request.Header.Set("Idempotency-Key", "quick-change")
	starter := &recordingRunStarter{}
	New(&recordingProjectService{}, features, firstBriefWorkflowService{workflowService},
		&recordingExecutionService{runErr: execution.ErrNotFound}, nil, starter).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "agents_not_chosen") {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if workflowService.state != "" || starter.runID != "" {
		t.Fatalf("a refused start changed the work order: state=%q run=%q", workflowService.state, starter.runID)
	}

	// The same Start naming a lead runs it; the skipped reviewer is the lead.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/runs",
		strings.NewReader(`{"steps":{"clarification":false,"documented_plan":false,"reviewer":false},`+
			`"agent_providers":{"lead_agent":"codex"},"agent_models":{"lead":"gpt-6-luna"}}`))
	request.Header.Set("Idempotency-Key", "quick-change-2")
	readying := &readyingFeatureService{recordingFeatureService: features, workflow: workflowService}
	NewWithWorkspaceRealWorkflowDeletionAndModels(&recordingProjectService{}, readying, firstBriefWorkflowService{workflowService},
		&recordingExecutionService{runErr: execution.ErrNotFound}, nil, starter, nil, nil, nil, offeredModels, nil, nil,
		testAgents()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if starter.providers.ReviewerAgent != "codex" || starter.models.Reviewer != "gpt-6-luna" {
		t.Fatalf("the run did not name the lead as the skipped reviewer: %+v %+v", starter.providers, starter.models)
	}
}

// A new app takes no agents from another project: it is created without
// any, and the assistant does its work (ADR-027). With no agent at all,
// nothing is created and the reason says what to set up.
func TestCreatingAnAppNeedsNoTemplateProject(t *testing.T) {
	action := conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateApp, Reason: "Asked", Title: "Tips", Description: "A tip calculator.",
		},
	}
	approve := func(stub *conversationStub, agents *recordingAgentService, projects ProjectService) *httptest.ResponseRecorder {
		handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub, agents)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/assistant/conversations/commitarium/actions/act_1/approve", strings.NewReader(`{}`)))
		return recorder
	}

	projects := &recordingProjectService{result: project.Project{ID: "prj_tips", Name: "Tips"}}
	recorder := approve(&conversationStub{action: action}, &recordingAgentService{agents: map[string]agent.Agent{}}, projects)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "no_agents") || projects.receivedName != "" {
		t.Fatalf("an app was created without agents: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = approve(&conversationStub{action: action}, testAgents(), projects)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "assistant_not_set_up") || projects.receivedName != "" {
		t.Fatalf("an app was created without the assistant's agent: %d %s", recorder.Code, recorder.Body.String())
	}

	stub := &conversationStub{action: action, settings: conversations.Settings{Agent: "claude", Model: "claude-sonnet-5-5"}}
	modes := &assistantModeProjects{recordingProjectService: projects}
	approve(stub, testAgents(), modes)
	if projects.receivedName != "Tips" || !projects.receivedAgentProviders.IsZero() {
		t.Fatalf("the app was not created on its own: name=%q agents=%+v", projects.receivedName, projects.receivedAgentProviders)
	}
	// The user asked the top-level assistant for it, so it builds everything.
	if modes.received != project.AssistantModeBuilds {
		t.Fatalf("the new app's assistant = %q, want builds", modes.received)
	}
}
