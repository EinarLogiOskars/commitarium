package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type assistantWorkerStub struct {
	attempt workerhttp.Attempt
	puts    []workerhttp.PutAttemptRequest
	forces  int
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

func (worker *assistantWorkerStub) ForceStop(_ context.Context, identity workerhttp.MutationIdentity, _ workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	worker.forces++
	worker.attempt.AttemptReference = identity.AttemptReference
	worker.attempt.State = workerhttp.AttemptStateTerminal
	return worker.attempt, nil
}

func TestAssistantProjectDeletionRefusesActiveUnlessForcedAndKeepsOtherProjects(t *testing.T) {
	root, workspaces := t.TempDir(), t.TempDir()
	reader := &projectReaderStub{stored: project.Project{ID: "prj_one", Name: "One"}}
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
	first, _, err := assistant.Start(t.Context(), "prj_one", project.AgentProviderCodex,
		"gpt-5.6-sol", "First", AssistantPurposeDesignStack, "start-one")
	if err != nil {
		t.Fatalf("start first assistant: %v", err)
	}
	reader.stored = project.Project{ID: "prj_two", Name: "Two"}
	second, _, err := assistant.Start(t.Context(), "prj_two", project.AgentProviderCodex,
		"gpt-5.6-sol", "Second", AssistantPurposeDesignStack, "start-two")
	if err != nil {
		t.Fatalf("start second assistant: %v", err)
	}
	if err := assistant.DeleteProject(t.Context(), "prj_one", "delete-one", false); !errors.Is(err, projectdeletion.ErrActive) {
		t.Fatalf("expected active assistant refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assistants", first.ID+".json")); err != nil {
		t.Fatalf("refused delete removed first record: %v", err)
	}
	if err := assistant.DeleteProject(t.Context(), "prj_one", "delete-one", true); err != nil {
		t.Fatalf("force-delete first assistants: %v", err)
	}
	if worker.forces != 1 {
		t.Fatalf("force-stop calls=%d want=1", worker.forces)
	}
	for _, path := range []string{
		filepath.Join(root, "assistants", first.ID+".json"),
		filepath.Join(workspaces, first.ID),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("project-one artifact remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "assistants", second.ID+".json"),
		filepath.Join(workspaces, second.ID),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("other-project artifact removed at %s: %v", path, err)
		}
	}
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
		"gpt-5.6-sol", "A small web API", AssistantPurposeDesignStack, "start-1")
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
		"gpt-5.6-sol", "A small web API", AssistantPurposeDesignStack, "start-1"); err != nil || created {
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
	first, _, err := newAssistant().Start(t.Context(), "prj_test", project.AgentProviderCodex, "gpt-5.6-sol", "API", AssistantPurposeDesignStack, "same-key")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, created, err := newAssistant().Start(t.Context(), "prj_test", project.AgentProviderCodex, "gpt-5.6-sol", "API", AssistantPurposeDesignStack, "same-key")
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("replay first=%+v second=%+v created=%t err=%v", first, second, created, err)
	}
	if _, _, err := newAssistant().Start(t.Context(), "prj_test", project.AgentProviderCodex, "gpt-5.6-sol", "different", AssistantPurposeDesignStack, "same-key"); err != ErrAssistantConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestAssistantVerifiesPinnedRepositoryEvidenceAndRejectsStaleApply(t *testing.T) {
	root, workspaces := t.TempDir(), t.TempDir()
	reader := &projectReaderStub{
		stored:   project.Project{ID: "prj_test", Name: "Imported app"},
		overview: project.RepositoryOverview{Head: project.RepositoryHead{CommitID: "commit_one"}},
		evidence: project.RepositoryToolchainEvidence{
			DefaultBranch: "main", CommitID: "commit_one",
			Files:          []project.RepositoryEvidenceFile{{Path: "backend/pyproject.toml", Content: "[project]\nname='app'\n"}},
			LanguageCounts: map[string]int{"Python": 8},
		},
	}
	manager, err := NewManager(root, reader)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	worker := &assistantWorkerStub{}
	assistant, err := NewAssistant(root, workspaces, reader, manager, map[project.AgentProvider]AssistantWorker{
		project.AgentProviderClaude: {Service: worker, AgentProfileID: "profile_claude"},
	})
	if err != nil {
		t.Fatalf("create assistant: %v", err)
	}

	session, created, err := assistant.Start(
		t.Context(), "prj_test", project.AgentProviderClaude, "claude-opus-4-8",
		"Please verify the detected stack.", AssistantPurposeVerifyRepository, "verify-1",
	)
	if err != nil || !created || session.Purpose != AssistantPurposeVerifyRepository ||
		session.VerifiedCommitID != "commit_one" || len(worker.puts) != 1 {
		t.Fatalf("start session=%+v created=%t puts=%d err=%v", session, created, len(worker.puts), err)
	}
	instructions := worker.puts[0].Instructions
	if !strings.Contains(instructions, "backend/pyproject.toml") ||
		!strings.Contains(instructions, "untrusted quoted data") || strings.Contains(instructions, "FORGEJO_TOKEN") {
		t.Fatalf("unsafe or incomplete verification instructions %q", instructions)
	}

	worker.attempt.State = workerhttp.AttemptStateTerminal
	worker.attempt.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary:           "This is a Python application. The project metadata pins the runtime requirements.",
		ToolchainProposal: &workerhttp.ToolchainProposal{Tools: map[string]string{"python": "3.14.7"}, Services: []string{}},
	}
	session, err = assistant.Get(t.Context(), "prj_test", session.ID)
	if err != nil || session.Status != AssistantStatusProposalReady || session.Proposal == nil ||
		session.Proposal.Confidence != "agent_verified" ||
		len(session.Proposal.Evidence) != 1 || session.Proposal.Evidence[0] != "backend/pyproject.toml" {
		t.Fatalf("verified session=%+v err=%v", session, err)
	}

	reader.overview.Head.CommitID = "commit_two"
	if _, err := assistant.Apply(t.Context(), "prj_test", session.ID); !errors.Is(err, ErrAssistantStale) {
		t.Fatalf("stale apply error=%v", err)
	}
	reader.overview.Head.CommitID = "commit_one"
	manifest, err := assistant.Apply(t.Context(), "prj_test", session.ID)
	if err != nil || manifest.Tools["python"] != "3.14.7" {
		t.Fatalf("apply verified manifest=%+v err=%v", manifest, err)
	}
}
