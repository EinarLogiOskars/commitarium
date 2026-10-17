package conversations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type workerStub struct {
	attempt workerhttp.Attempt
	puts    []workerhttp.PutAttemptRequest
}

func (worker *workerStub) PutAttempt(_ context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error) {
	// A real worker refuses a request that fails its contract.
	if err := request.Validate(identity); err != nil {
		return workerhttp.Attempt{}, false, err
	}
	worker.puts = append(worker.puts, request)
	worker.attempt = workerhttp.Attempt{
		AttemptReference: identity.AttemptReference, Mode: request.Mode, Assignment: request.Assignment,
		ProviderSessionID: "thread", State: workerhttp.AttemptStateRunning,
	}
	return worker.attempt, true, nil
}

func (worker *workerStub) GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	return worker.attempt, nil
}

func (*workerStub) SendCommand(context.Context, workerhttp.MutationIdentity, workerhttp.CommandRequest) (workerhttp.Attempt, error) {
	panic("unexpected command")
}

func (*workerStub) ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	panic("unexpected force stop")
}

func (*workerStub) Supersede(context.Context, workerhttp.MutationIdentity, workerhttp.SupersedeRequest) (workerhttp.Attempt, error) {
	panic("unexpected supersede")
}

func (worker *workerStub) finish(result workerhttp.TerminalResult) {
	worker.attempt.State = workerhttp.AttemptStateTerminal
	worker.attempt.Result = &result
}

type projectsStub struct{}

func (projectsStub) GetByID(_ context.Context, id string) (project.Project, error) {
	return project.Project{
		ID: id, Name: "Calories",
		AgentProviders: project.AgentProviders{LeadAgent: "codex", ReviewerAgent: "claude"},
		AgentModels:    project.AgentModels{Lead: "gpt-5.6-sol", Reviewer: "claude-opus-5-5"},
	}, nil
}

func (projectsStub) List(context.Context) ([]project.Project, error) {
	return []project.Project{{ID: "prj_test", Name: "Calories"}, {ID: "prj_todo", Name: "To-do"}}, nil
}

func (projectsStub) GetRepositoryOverview(context.Context, string) (project.RepositoryOverview, error) {
	return project.RepositoryOverview{}, nil
}

type agentsStub struct{}

func (agentsStub) List(context.Context) ([]agent.Agent, error) {
	return []agent.Agent{{ID: "codex", Name: "Codex", Provider: project.AgentProviderCodex}, {ID: "claude", Name: "Claude", Provider: project.AgentProviderClaude}}, nil
}

type describerStub struct{}

func (describerStub) Describe(context.Context, string) (string, error) {
	return "## This project: Calories", nil
}

// newTestService is an assistant the user set up on the codex agent.
func newTestService(t *testing.T) (*Service, *workerStub) {
	t.Helper()
	service, worker := newUnsetService(t, agentsStub{})
	if _, err := service.SaveSettings(t.Context(), Settings{
		Agent: "codex", Model: "codex-default", AskBefore: DefaultAskBefore,
	}); err != nil {
		t.Fatal(err)
	}
	return service, worker
}

// newUnsetService is an assistant whose agent the user has not chosen.
func newUnsetService(t *testing.T, agents AgentLister) (*Service, *workerStub) {
	t.Helper()
	worker := &workerStub{}
	service, err := NewService(Config{
		Root: t.TempDir(), WorkspaceRoot: t.TempDir(), Projects: projectsStub{}, Agents: agents,
		Describer: describerStub{},
		Workers:   func(string) (workerhttp.Service, error) { return worker, nil },
		DefaultModel: func(_ context.Context, provider project.AgentProvider) string {
			return string(provider) + "-default"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, worker
}

type noAgents struct{}

func (noAgents) List(context.Context) ([]agent.Agent, error) { return []agent.Agent{}, nil }

// Without an agent, or without one chosen for it, the assistant says what to
// set up instead of failing a turn (ADR-027).
func TestTheAssistantSaysWhatToSetUp(t *testing.T) {
	empty, worker := newUnsetService(t, noAgents{})
	if _, err := empty.Send(t.Context(), TopLevel, "Hi", "msg-1"); !errors.Is(err, ErrNoAgents) || len(worker.puts) != 0 {
		t.Fatalf("send without agents: err=%v puts=%d", err, len(worker.puts))
	}
	unset, worker := newUnsetService(t, agentsStub{})
	if settings, err := unset.Settings(t.Context()); err != nil || settings.Agent != "" || settings.Model != "" {
		t.Fatalf("an assistant nobody set up picked an agent: %+v err=%v", settings, err)
	}
	if _, err := unset.Send(t.Context(), TopLevel, "Hi", "msg-1"); !errors.Is(err, ErrNotSetUp) || len(worker.puts) != 0 {
		t.Fatalf("send without a chosen agent: err=%v puts=%d", err, len(worker.puts))
	}
	if _, err := unset.SaveSettings(t.Context(), Settings{Agent: "claude", Model: "claude-sonnet-5-5"}); err != nil {
		t.Fatal(err)
	}
	if _, err := unset.Send(t.Context(), TopLevel, "Hi", "msg-2"); err != nil || len(worker.puts) != 1 {
		t.Fatalf("send once set up: err=%v puts=%d", err, len(worker.puts))
	}
}

// An assistant that answered before ADR-027 without a saved choice keeps the
// default it had then, saved once.
func TestAnAssistantThatAnsweredKeepsItsEarlierDefault(t *testing.T) {
	service, _ := newUnsetService(t, agentsStub{})
	if err := service.write(record{Conversation: Conversation{Scope: "prj_test", Status: StatusIdle}, Turn: 3}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.Settings(t.Context())
	if err != nil || settings.Agent != "codex" || settings.Model != "codex-default" {
		t.Fatalf("earlier default = %+v err=%v", settings, err)
	}
	if _, err := service.Readiness(t.Context()); err != nil {
		t.Fatalf("an assistant on its earlier default is not ready: %v", err)
	}
}

func TestAnAssistantTurnProposesActionsAndKeepsNotes(t *testing.T) {
	service, worker := newTestService(t)
	conversation, err := service.Send(t.Context(), "prj_test", "What should we build next?", "msg-1")
	if err != nil || conversation.Status != StatusRunning {
		t.Fatalf("send = %+v err=%v", conversation, err)
	}
	first := worker.puts[0]
	if first.Mode != workerhttp.AttemptModeStart || first.OutputContract != workerhttp.OutputContractAssistantTurn ||
		first.Assignment.Role != workerhttp.RoleConsultant || first.WorkspaceAccess != workerhttp.WorkspaceAccessReadOnly ||
		first.Assignment.AgentProfileID != "codex" || first.Assignment.Model != "codex-default" ||
		!strings.Contains(first.Instructions, "What should we build next?") ||
		!strings.Contains(first.Instructions, "fable, astra") {
		t.Fatalf("unexpected first turn: %+v", first)
	}
	if queued, err := service.Send(t.Context(), "prj_test", "Hello?", "msg-2"); err != nil ||
		len(queued.Queued) != 1 || queued.Queued[0].Text != "Hello?" || len(worker.puts) != 1 {
		t.Fatalf("a message while answering was not queued: %+v err=%v", queued, err)
	}

	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Two work orders finish P0.",
		AssistantTurn: &featureartifact.AssistantTurn{Notes: "Prefers quick changes.", Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Small form", Title: "Log a meal", Steps: "quick",
				LeadAgent: "codex", LeadModel: "gpt-6-luna", ReviewerAgent: "claude", ReviewerModel: "claude-opus-5-5"},
			{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Tricky sync", Title: "Sync", Steps: "full",
				LeadAgent: "claude", LeadModel: "claude-fable-5-1", ReviewerAgent: "codex", ReviewerModel: "gpt-6-luna"},
		}},
	})
	conversation, err = service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusIdle || len(conversation.Actions) != 2 || conversation.Notes != "Prefers quick changes." {
		t.Fatalf("after the turn = %+v err=%v", conversation, err)
	}
	quick, full := conversation.Actions[0], conversation.Actions[1]
	// The cards show the assistant's own choice; a skipped reviewer is dropped
	// and nothing comes from the project (ADR-027).
	if quick.LeadAgent != "codex" || quick.LeadModel != "gpt-6-luna" || quick.ReviewerAgent != "" || quick.ReviewerModel != "" ||
		full.LeadAgent != "claude" || full.LeadModel != "claude-fable-5-1" || full.ReviewerAgent != "codex" || full.ReviewerModel != "gpt-6-luna" {
		t.Fatalf("a new work order's card does not show what will run: %+v / %+v", quick.AssistantAction, full.AssistantAction)
	}
	if len(conversation.Actions[0].Premium) != 0 || len(conversation.Actions[1].Premium) != 1 {
		t.Fatalf("premium marks = %+v / %+v", conversation.Actions[0].Premium, conversation.Actions[1].Premium)
	}
	resolved, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[0].ID, ActionApplied, "Created the work order Log a meal.", Created{WorkOrderID: "fea_log"})
	if err != nil || resolved.Actions[0].Status != ActionApplied || resolved.Actions[0].Result == "" || resolved.Actions[0].WorkOrderID != "fea_log" {
		t.Fatalf("resolve = %+v err=%v", resolved, err)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[0].ID, ActionDismissed, "", Created{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("an applied action was resolved again: %v", err)
	}
}

func TestChangingTheAssistantsAgentStartsAFreshSession(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "First", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Hi."})
	if _, err := service.Send(t.Context(), "prj_test", "Second", "msg-2"); err != nil {
		t.Fatal(err)
	}
	if worker.puts[1].Mode != workerhttp.AttemptModeResume || worker.puts[1].ProviderSessionID != "thread" {
		t.Fatalf("the second turn did not resume: %+v", worker.puts[1])
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Sure."})
	if _, err := service.SaveSettings(t.Context(), Settings{Agent: "claude", Model: "claude-sonnet-5-5", AskBefore: []string{"fable"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Third", "msg-3"); err != nil {
		t.Fatal(err)
	}
	third := worker.puts[2]
	if third.Mode != workerhttp.AttemptModeStart || third.Assignment.AgentProfileID != "claude" ||
		!strings.Contains(third.Instructions, "Second") {
		t.Fatalf("a new agent did not start fresh with the recent conversation: %+v", third)
	}
}

func TestAUsageLimitTellsTheUserWhenToTryAgain(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Hi", "msg-1"); err != nil {
		t.Fatal(err)
	}
	resetsAt := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed,
		Error: &workerhttp.ProtocolError{Code: workerhttp.ErrorUsageLimit, Message: "limit", Retryable: true, ResetsAt: &resetsAt}})
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusIdle ||
		!strings.Contains(conversation.Messages[len(conversation.Messages)-1].Text, "usage limit") {
		t.Fatalf("limit = %+v err=%v", conversation, err)
	}
}

func TestPremiumFamiliesMatchAnyVersion(t *testing.T) {
	settings := Settings{AskBefore: DefaultAskBefore}
	for model, premium := range map[string]bool{
		"claude-fable-5-1": true, "gpt-6-astra": true, "claude-fable-6": true, "gpt-6-luna": false, "": false,
	} {
		if settings.RequiresApproval(model) != premium {
			t.Errorf("%q premium = %t", model, !premium)
		}
	}
}

func TestTheTopLevelConversationReachesItsAgent(t *testing.T) {
	service, worker := newTestService(t)
	conversation, err := service.Send(t.Context(), TopLevel, "I'd like a new app: a Funko Pop tracker", "msg-1")
	if err != nil || conversation.Status != StatusRunning || len(worker.puts) != 1 {
		t.Fatalf("top-level send = %+v err=%v puts=%d", conversation, err, len(worker.puts))
	}
	if projectID := worker.puts[0].Assignment.ProjectID; projectID != TopLevel {
		t.Fatalf("top-level project ID = %q, want %q", projectID, TopLevel)
	}
}

type guideStore struct {
	overrides map[string]modelguide.Override
}

func (store *guideStore) ListModelGuideOverrides(context.Context) ([]modelguide.Override, error) {
	listed := make([]modelguide.Override, 0, len(store.overrides))
	for _, override := range store.overrides {
		listed = append(listed, override)
	}
	return listed, nil
}

func (store *guideStore) SaveModelGuideOverride(_ context.Context, override modelguide.Override) error {
	store.overrides[override.ModelID] = override
	return nil
}

func (store *guideStore) DeleteModelGuideOverride(_ context.Context, id string) error {
	delete(store.overrides, id)
	return nil
}

// With a model guide, approvals follow its Premium and unrated models, and
// the families the user stored before are migrated into it once (ADR-031).
func TestTheModelGuideDecidesApprovalsAndMigratesFamilies(t *testing.T) {
	store := &guideStore{overrides: map[string]modelguide.Override{}}
	guide, err := modelguide.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	offered := []ModelChoice{}
	root := t.TempDir()
	service, err := NewService(Config{
		Root: root, WorkspaceRoot: t.TempDir(), Projects: projectsStub{}, Agents: agentsStub{},
		Describer: describerStub{},
		Workers:   func(string) (workerhttp.Service, error) { return &workerStub{}, nil },
		Guide:     guide,
		Models:    func(context.Context) []ModelChoice { return offered },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Settings saved before the guide: the user added "opus".
	if err := writeJSON(service.settingsPath(), Settings{
		Agent: "codex", Model: "codex-default", AskBefore: []string{"fable", "astra", "opus"},
	}); err != nil {
		t.Fatal(err)
	}

	// Until the agents' models are known, the families wait.
	if settings, err := service.Settings(t.Context()); err != nil || settings.AskBeforeMigrated || len(store.overrides) != 0 {
		t.Fatalf("early settings = %+v err=%v overrides=%+v", settings, err, store.overrides)
	}

	offered = []ModelChoice{
		{Provider: project.AgentProviderClaude, ID: "claude-opus-5-5"},
		{Provider: project.AgentProviderClaude, ID: "claude-sonnet-5-5"},
		{Provider: project.AgentProviderCodex, ID: "brand-new-model"},
	}
	settings, err := service.Settings(t.Context())
	if err != nil || !settings.AskBeforeMigrated {
		t.Fatalf("settings = %+v err=%v", settings, err)
	}
	if store.overrides["claude-opus-5-5"].Tier != modelguide.TierPremium {
		t.Fatalf("overrides = %+v", store.overrides)
	}
	for _, want := range []string{"claude-fable-5-1", "gpt-6-astra", "claude-opus-5-5", "brand-new-model"} {
		if !slices.Contains(settings.AskBefore, want) {
			t.Errorf("ask_before %v lacks %s", settings.AskBefore, want)
		}
	}
	if slices.Contains(settings.AskBefore, "claude-sonnet-5-5") {
		t.Errorf("ask_before %v has sonnet", settings.AskBefore)
	}
	for model, want := range map[string]bool{
		"claude-opus-5-5": true, "claude-sonnet-5-5": false, "brand-new-model": true, "": false,
	} {
		if got := service.RequiresApproval(t.Context(), model); got != want {
			t.Errorf("RequiresApproval(%q) = %t", model, got)
		}
	}

	// The migration is recorded, and a later save keeps the stored families
	// instead of the guide's list.
	stored, err := service.storedSettings(t.Context())
	if err != nil || !stored.AskBeforeMigrated {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	if _, err := service.SaveSettings(t.Context(), Settings{Agent: "codex", Model: "codex-default", AskBefore: settings.AskBefore}); err != nil {
		t.Fatal(err)
	}
	if stored, _ := service.storedSettings(t.Context()); !stored.AskBeforeMigrated || slices.Contains(stored.AskBefore, "brand-new-model") {
		t.Fatalf("saved = %+v", stored)
	}
}

type chatUsageStub struct{ recorded []ChatUsage }

func (stub *chatUsageStub) RecordAssistantChatUsage(_ context.Context, usage ChatUsage) error {
	stub.recorded = append(stub.recorded, usage)
	return nil
}

// Each finished assistant turn's usage is recorded under its scope, so a
// project's cost includes the assistant (ADR-031).
func TestAFinishedTurnRecordsItsUsage(t *testing.T) {
	service, worker := newTestService(t)
	recorder := &chatUsageStub{}
	service.config.ChatUsage = recorder
	if _, err := service.Send(t.Context(), "prj_test", "Hi", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Hello.",
		Usage: &workerhttp.TokenUsage{InputTokens: 12, OutputTokens: 3, Billing: "subscription"},
	})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.recorded) == 0 {
		t.Fatal("the turn's usage was not recorded")
	}
	got := recorder.recorded[0]
	if got.Scope != "prj_test" || got.Agent != "codex" || got.Model != "codex-default" ||
		got.Usage.InputTokens != 12 || got.Usage.Billing != "subscription" || got.AttemptID == "" {
		t.Fatalf("recorded = %+v", got)
	}
}
