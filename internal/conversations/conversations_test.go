package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type workerStub struct {
	attempt workerhttp.Attempt
	puts    []workerhttp.PutAttemptRequest
}

func (worker *workerStub) PutAttempt(_ context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error) {
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

func newTestService(t *testing.T) (*Service, *workerStub) {
	t.Helper()
	worker := &workerStub{}
	service, err := NewService(Config{
		Root: t.TempDir(), WorkspaceRoot: t.TempDir(), Projects: projectsStub{}, Agents: agentsStub{},
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
			{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Small form", Title: "Log a meal", Steps: "quick", LeadModel: "gpt-6-luna"},
			{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Tricky sync", Title: "Sync", Steps: "full", LeadModel: "claude-fable-5-1"},
		}},
	})
	conversation, err = service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusIdle || len(conversation.Actions) != 2 || conversation.Notes != "Prefers quick changes." {
		t.Fatalf("after the turn = %+v err=%v", conversation, err)
	}
	quick, full := conversation.Actions[0], conversation.Actions[1]
	if quick.LeadAgent != "codex" || quick.LeadModel != "gpt-6-luna" || quick.ReviewerModel != "" ||
		full.LeadModel != "claude-fable-5-1" || full.ReviewerAgent != "claude" || full.ReviewerModel != "claude-opus-5-5" {
		t.Fatalf("a new work order's card does not show what will run: %+v / %+v", quick.AssistantAction, full.AssistantAction)
	}
	if len(conversation.Actions[0].Premium) != 0 || len(conversation.Actions[1].Premium) != 1 {
		t.Fatalf("premium marks = %+v / %+v", conversation.Actions[0].Premium, conversation.Actions[1].Premium)
	}
	resolved, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[0].ID, ActionApplied, "Created the work order Log a meal.", "fea_log")
	if err != nil || resolved.Actions[0].Status != ActionApplied || resolved.Actions[0].Result == "" || resolved.Actions[0].WorkOrderID != "fea_log" {
		t.Fatalf("resolve = %+v err=%v", resolved, err)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[0].ID, ActionDismissed, "", ""); !errors.Is(err, ErrNotPending) {
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
