package assistant_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const baseCommit = "0123456789abcdef0123456789abcdef01234567"

type scriptedWorker struct {
	mu       sync.Mutex
	puts     []workerhttp.PutAttemptRequest
	attempts map[string]workerhttp.Attempt
}

func (worker *scriptedWorker) PutAttempt(
	_ context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if attempt, ok := worker.attempts[identity.AttemptID]; ok {
		return attempt, false, nil
	}
	worker.puts = append(worker.puts, request)
	attempt := workerhttp.Attempt{
		AttemptReference: identity.AttemptReference, ProviderSessionID: "provider-thread",
		State: workerhttp.AttemptStateRunning,
	}
	worker.attempts[identity.AttemptID] = attempt
	return attempt, true, nil
}

func (worker *scriptedWorker) GetAttempt(_ context.Context, reference workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	attempt, ok := worker.attempts[reference.AttemptID]
	if !ok {
		return workerhttp.Attempt{}, errors.New("unknown attempt")
	}
	return attempt, nil
}

func (worker *scriptedWorker) finish(attemptID string, result workerhttp.TerminalResult) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	attempt := worker.attempts[attemptID]
	attempt.State = workerhttp.AttemptStateTerminal
	attempt.Result = &result
	worker.attempts[attemptID] = attempt
}

type workspaceStub struct{}

func (workspaceStub) PrepareForClarification(_ context.Context, projectID, featureID string) (workspace.Workspace, bool, error) {
	return workspace.Workspace{ID: "wsp_" + featureID, ProjectID: projectID, FeatureID: featureID, BaseCommitID: baseCommit}, true, nil
}

func newAssistant(t *testing.T) (*assistant.Service, *scriptedWorker, *workflow.Service, feature.Feature) {
	t.Helper()
	db, err := database.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	projects := project.NewService(database.NewProjectStore(db))
	storedProject, err := projects.Create(
		t.Context(), "Assistant test", project.RecoveryPolicyApprovalRequired,
		project.DefaultDialogueLimits(), project.DefaultAgentProviders(), project.DefaultMergePolicy(),
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	features := feature.NewService(database.NewFeatureStore(db), projects)
	storedFeature, err := features.Create(
		t.Context(), storedProject.ID, "Add due dates", "Let users set a due date on to-dos.", feature.SettingsOverrides{},
	)
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}
	worker := &scriptedWorker{attempts: map[string]workerhttp.Attempt{}}
	briefs := workflow.NewService(database.NewWorkflowStore(db))
	service, err := assistant.NewService(assistant.Config{
		Store: database.NewAssistantStore(db), Features: features,
		Workspaces: workspaceStub{}, Briefs: briefs,
		Workers: map[project.AgentProvider]assistant.Worker{
			project.AgentProviderClaude: {Service: worker, AgentProfileID: "claude-default"},
		},
		Now: func() time.Time { return time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("create assistant: %v", err)
	}
	return service, worker, briefs, storedFeature
}

func TestAssistantClarifiesAWorkOrderIntoAHandoffBrief(t *testing.T) {
	service, worker, briefs, storedFeature := newAssistant(t)
	session, created, err := service.Start(t.Context(), storedFeature.ProjectID, storedFeature.ID, project.AgentProviderClaude, "")
	if err != nil || !created || session.Status != assistant.StatusRunning {
		t.Fatalf("start: session=%+v created=%v err=%v", session, created, err)
	}
	first := worker.puts[0]
	if first.Mode != workerhttp.AttemptModeStart || first.Assignment.Role != workerhttp.RoleConsultant ||
		first.WorkspaceAccess != workerhttp.WorkspaceAccessReadOnly ||
		first.OutputContract != workerhttp.OutputContractWorkOrderBrief ||
		first.Assignment.WorkspaceID != "wsp_"+storedFeature.ID ||
		!strings.Contains(first.Instructions, "Let users set a due date on to-dos.") {
		t.Fatalf("first turn request: %+v", first)
	}
	if again, created, err := service.Start(t.Context(), storedFeature.ProjectID, storedFeature.ID, project.AgentProviderClaude, ""); err != nil ||
		created || again.ID != session.ID || len(worker.puts) != 1 {
		t.Fatalf("restart: session=%+v created=%v err=%v puts=%d", again, created, err, len(worker.puts))
	}

	worker.finish(session.ID+":turn:1", workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
		Summary: "Should overdue to-dos be highlighted?",
		Usage:   &workerhttp.TokenUsage{InputTokens: 10, OutputTokens: 5},
	})
	session, err = service.Get(t.Context(), storedFeature.ProjectID, storedFeature.ID)
	if err != nil || session.Status != assistant.StatusWaitingForUser || len(session.Messages) != 2 ||
		session.Messages[1].Text != "Should overdue to-dos be highlighted?" {
		t.Fatalf("question: session=%+v err=%v", session, err)
	}

	session, created, err = service.Reply(t.Context(), storedFeature.ProjectID, storedFeature.ID, "Yes, in red.", "reply-1")
	if err != nil || !created || session.Status != assistant.StatusRunning || len(worker.puts) != 2 ||
		worker.puts[1].Mode != workerhttp.AttemptModeResume || worker.puts[1].ProviderSessionID != "provider-thread" {
		t.Fatalf("reply: session=%+v created=%v err=%v puts=%+v", session, created, err, worker.puts)
	}
	if _, created, err := service.Reply(t.Context(), storedFeature.ProjectID, storedFeature.ID, "Yes, in red.", "reply-1"); err != nil ||
		created || len(worker.puts) != 2 {
		t.Fatalf("repeated reply: created=%v err=%v puts=%d", created, err, len(worker.puts))
	}
	if _, _, err := service.Reply(t.Context(), storedFeature.ProjectID, storedFeature.ID, "Something else.", "reply-1"); !errors.Is(err, assistant.ErrConflict) {
		t.Fatalf("reused key with a different message: %v", err)
	}

	worker.finish(session.ID+":turn:2", workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Here is the brief.",
		HandoffBrief: &featureartifact.HandoffBrief{
			Goal:  "Add optional due dates and highlight overdue to-dos in red.",
			Areas: []string{"backend/app/models: due_date column"}, Considerations: []string{"Existing rows stay valid."},
			OpenQuestions: []string{},
		},
	})
	session, err = service.Get(t.Context(), storedFeature.ProjectID, storedFeature.ID)
	if err != nil || session.Status != assistant.StatusProposalReady {
		t.Fatalf("proposal: session=%+v err=%v", session, err)
	}
	artifact, err := briefs.GetFeatureArtifact(t.Context(), storedFeature.ID, featureartifact.KindHandoffBrief)
	if err != nil {
		t.Fatalf("load brief: %v", err)
	}
	brief := featureartifact.HandoffBrief{}
	if err := json.Unmarshal([]byte(artifact.Document), &brief); err != nil || brief.BaseCommitID != baseCommit ||
		!strings.Contains(brief.Goal, "overdue") {
		t.Fatalf("brief=%+v err=%v", brief, err)
	}
}
