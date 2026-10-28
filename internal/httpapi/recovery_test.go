package httpapi

import (
	"context"
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

// blockedExecutions is a run waiting on a blocker, with its recoveries.
type blockedExecutions struct {
	*recordingExecutionService
	blocker    execution.Blocker
	recoveries []execution.Recovery
}

func (stub *blockedExecutions) RunBlocker(context.Context, string) (execution.Blocker, bool, error) {
	return stub.blocker, true, nil
}

func (stub *blockedExecutions) RunRecoveries(context.Context, string) ([]execution.Recovery, error) {
	return stub.recoveries, nil
}

// The assistant recovers by the way it chose, and the coordinator refuses a
// way already tried on the same failure and caps the tries (ADR-032).
func TestRecoveringAWorkOrderIsBoundedByTheFailure(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantRecoverWorkOrder, Reason: "Check may pass now",
			WorkOrderID: "fea_log", How: "recheck",
		},
	}}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_log", ProjectID: "prj_test", Title: "Show the log", CreatedBy: feature.CreatedByAssistant,
	}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	executions := &blockedExecutions{
		recordingExecutionService: &recordingExecutionService{runs: []execution.Run{{ID: "run_1", StartedAt: time.Now(), Status: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindBlocker}}},
		blocker:                   execution.Blocker{RunID: "run_1", Kind: execution.BlockerCheck, Fingerprint: "fp1"},
	}
	workflow := &planningStarterStub{run: execution.Run{ID: "run_1"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, workflow, nil, nil, nil, nil, stub)
	apply := func() error {
		stub.action.Status = conversations.ActionProposed
		return AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1")
	}

	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if workflow.receivedRecovery.Action != orchestration.RecoveryActionRecheck || !workflow.receivedRecovery.FromAssistant {
		t.Fatalf("recovery = %+v", workflow.receivedRecovery)
	}

	executions.recoveries = []execution.Recovery{{How: execution.RecoveryRecheck, Fingerprint: "fp1"}}
	if err := apply(); err == nil || !strings.Contains(err.Error(), "already tried on this same failure") {
		t.Fatalf("a repeated recheck = %v", err)
	}
	// Another failure's recheck does not count against this one.
	executions.recoveries = []execution.Recovery{{How: execution.RecoveryRecheck, Fingerprint: "fp0"}}
	if err := apply(); err != nil {
		t.Fatalf("a recheck after another failure's = %v", err)
	}
	executions.recoveries = []execution.Recovery{
		{How: execution.RecoveryContinue, Fingerprint: "fp1"}, {How: execution.RecoveryMessage, Fingerprint: "fp1"},
		{How: execution.RecoveryFreshConversation, Fingerprint: "fp1"}, {How: execution.RecoveryBackToReady, Fingerprint: "fp1"},
	}
	if err := apply(); err == nil || !strings.Contains(err.Error(), "as many recoveries as it gets") {
		t.Fatalf("past the cap = %v", err)
	}
}

// Recovery runs out after four tries of one failure, eight in all, or the
// same failure coming back after a fresh conversation (ADR-032).
func TestRecoveryRunsOutPlainly(t *testing.T) {
	executions := &blockedExecutions{
		recordingExecutionService: &recordingExecutionService{},
		blocker:                   execution.Blocker{Kind: execution.BlockerCheck, Fingerprint: "fp1"},
	}
	api := &API{execution: executions}
	if _, exhausted := api.recoveryExhausted(t.Context(), "run_1"); exhausted {
		t.Fatal("exhausted without recoveries")
	}
	executions.recoveries = []execution.Recovery{{How: execution.RecoveryFreshConversation, Fingerprint: "fp1"}}
	if _, exhausted := api.recoveryExhausted(t.Context(), "run_1"); !exhausted {
		t.Fatal("a check failing after a fresh conversation is not exhausted")
	}
	executions.blocker.Kind = execution.BlockerAgent
	if _, exhausted := api.recoveryExhausted(t.Context(), "run_1"); !exhausted {
		t.Fatal("an agent failure coming back after a fresh conversation is not exhausted")
	}
	executions.recoveries = []execution.Recovery{{How: execution.RecoveryMessage, Fingerprint: "fp1"}}
	if _, exhausted := api.recoveryExhausted(t.Context(), "run_1"); exhausted {
		t.Fatal("one message is exhausted")
	}
	for range 3 {
		executions.recoveries = append(executions.recoveries, execution.Recovery{How: execution.RecoveryContinue, Fingerprint: "fp1"})
	}
	if _, exhausted := api.recoveryExhausted(t.Context(), "run_1"); !exhausted {
		t.Fatal("four recoveries of one failure are not exhausted")
	}
	if detail := exhaustedDetail("Add audio", execution.BlockerCheck); !strings.Contains(detail, "a check on Commitarium's side keeps failing") {
		t.Fatalf("detail = %q", detail)
	}
}

// signedOutExecutions is a blocked run that waits for its agent's sign-in.
type signedOutExecutions struct {
	*blockedExecutions
}

func (stub *signedOutExecutions) LoginWaitForRun(context.Context, string) (execution.LoginWait, error) {
	return execution.LoginWait{AgentID: "claude"}, nil
}

// Autopilot spends no recovery on a run waiting for a sign-in: it continues
// by itself once the agent works (LOGIN_RECOVERY_PLAN.md).
func TestAutopilotLeavesARunWaitingForASignIn(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantRecoverWorkOrder, Reason: "Try again",
			WorkOrderID: "fea_log", How: "continue", Guidance: "Try again.",
		},
	}}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_log", ProjectID: "prj_test", Title: "Show the log", CreatedBy: feature.CreatedByAssistant,
	}}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	executions := &signedOutExecutions{&blockedExecutions{
		recordingExecutionService: &recordingExecutionService{runs: []execution.Run{{ID: "run_1", StartedAt: time.Now(), Status: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindBlocker}}},
		blocker:                   execution.Blocker{RunID: "run_1", Kind: execution.BlockerEnvironment, Fingerprint: "fp1"},
	}}
	workflow := &planningStarterStub{run: execution.Run{ID: "run_1"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, workflow, nil, nil, nil, nil, stub)
	err := AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1")
	if err == nil || !strings.Contains(err.Error(), "waits for agent claude to sign in again") ||
		workflow.receivedRecovery.Action != "" {
		t.Fatalf("autopilot recovered a run waiting for a sign-in: %v %+v", err, workflow.receivedRecovery)
	}
}
