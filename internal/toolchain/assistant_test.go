package toolchain

import (
	"context"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type assistantWorkerStub struct {
	attempt workerhttp.Attempt
	puts    []workerhttp.PutAttemptRequest
}

func (worker *assistantWorkerStub) PutAttempt(_ context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error) {
	worker.puts = append(worker.puts, request)
	now := time.Now().UTC()
	worker.attempt = workerhttp.Attempt{AttemptReference: identity.AttemptReference, Mode: request.Mode,
		Assignment: request.Assignment, ProviderSessionID: "provider_session_test", State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now}
	return worker.attempt, true, nil
}

func (worker *assistantWorkerStub) GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	return worker.attempt, nil
}

func (*assistantWorkerStub) SendCommand(context.Context, workerhttp.MutationIdentity, workerhttp.CommandRequest) (workerhttp.Attempt, error) {
	panic("unexpected command")
}

func (*assistantWorkerStub) ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	panic("unexpected stop")
}

func TestAssistantConversationAppliesValidatedProposal(t *testing.T) {
	root, workspaces := t.TempDir(), t.TempDir()
	reader := &projectReaderStub{stored: project.Project{ID: "prj_test", Name: "Example"}}
	manager, err := NewManager(root, reader)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	worker := &assistantWorkerStub{}
	assistant, err := NewAssistant(root, workspaces, reader, manager, map[project.AgentProvider]AssistantWorker{
		project.AgentProviderCodex: {Service: worker, AgentProfileID: "profile_codex"},
	})
	if err != nil {
		t.Fatalf("create assistant: %v", err)
	}

	session, created, err := assistant.Start(t.Context(), "prj_test", project.AgentProviderCodex,
		"gpt-5.6-sol", "A small web API", "start-1")
	if err != nil || !created || session.Status != AssistantStatusRunning || len(worker.puts) != 1 {
		t.Fatalf("start session=%+v created=%t puts=%d err=%v", session, created, len(worker.puts), err)
	}
	worker.attempt.State = workerhttp.AttemptStateTerminal
	worker.attempt.Result = &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted,
		Disposition: workerhttp.DispositionInputRequired, Summary: "Do you need a browser UI?"}
	session, err = assistant.Get(t.Context(), "prj_test", session.ID)
	if err != nil || session.Status != AssistantStatusWaitingForUser || session.Message == "" || len(session.Messages) != 2 {
		t.Fatalf("waiting session=%+v err=%v", session, err)
	}
	session, created, err = assistant.Reply(t.Context(), "prj_test", session.ID, "No, API only", "reply-1")
	if err != nil || !created || session.Status != AssistantStatusRunning || worker.puts[1].Mode != workerhttp.AttemptModeResume {
		t.Fatalf("reply session=%+v created=%t err=%v", session, created, err)
	}
	if _, created, err := assistant.Start(t.Context(), "prj_test", project.AgentProviderCodex,
		"gpt-5.6-sol", "A small web API", "start-1"); err != nil || created {
		t.Fatalf("original start was not replayable after reply: created=%t err=%v", created, err)
	}
	worker.attempt.State = workerhttp.AttemptStateTerminal
	worker.attempt.Result = &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted,
		Disposition: workerhttp.DispositionSucceeded, Summary: "Use Python and SQLite.",
		ToolchainProposal: &workerhttp.ToolchainProposal{Tools: map[string]string{"python": "3.14.7"}, Services: []string{"sqlite"}}}
	session, err = assistant.Get(t.Context(), "prj_test", session.ID)
	if err != nil || session.Status != AssistantStatusProposalReady || session.Proposal.Tools["python"] != "3.14.7" || len(session.Messages) != 4 {
		t.Fatalf("proposal session=%+v err=%v", session, err)
	}
	manifest, err := assistant.Apply(t.Context(), "prj_test", session.ID)
	if err != nil || manifest.Source != SourceAssistant || manifest.Tools["python"] != "3.14.7" {
		t.Fatalf("applied manifest=%+v err=%v", manifest, err)
	}
	manifest, err = assistant.Apply(t.Context(), "prj_test", session.ID)
	if err != nil || manifest.Tools["python"] != "3.14.7" {
		t.Fatalf("idempotent apply manifest=%+v err=%v", manifest, err)
	}
}

func TestAssistantStartIsDurablyIdempotent(t *testing.T) {
	root, workspaces := t.TempDir(), t.TempDir()
	reader := &projectReaderStub{stored: project.Project{ID: "prj_test", Name: "Example"}}
	manager, _ := NewManager(root, reader)
	worker := &assistantWorkerStub{}
	newAssistant := func() *Assistant {
		created, err := NewAssistant(root, workspaces, reader, manager, map[project.AgentProvider]AssistantWorker{
			project.AgentProviderCodex: {Service: worker, AgentProfileID: "profile_codex"},
		})
		if err != nil {
			t.Fatalf("create assistant: %v", err)
		}
		return created
	}
	first, _, err := newAssistant().Start(t.Context(), "prj_test", project.AgentProviderCodex, "gpt-5.6-sol", "API", "same-key")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, created, err := newAssistant().Start(t.Context(), "prj_test", project.AgentProviderCodex, "gpt-5.6-sol", "API", "same-key")
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("replay first=%+v second=%+v created=%t err=%v", first, second, created, err)
	}
	if _, _, err := newAssistant().Start(t.Context(), "prj_test", project.AgentProviderCodex, "gpt-5.6-sol", "different", "same-key"); err != ErrAssistantConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
}
