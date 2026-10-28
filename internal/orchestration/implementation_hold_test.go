package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// holdsStub holds every work order until it is released.
type holdsStub struct {
	mu       sync.Mutex
	released bool
}

func (stub *holdsStub) ImplementationHold(context.Context, string, string) (string, bool) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.released {
		return "", false
	}
	return execution.ImplementationHoldReason("Lay the foundation"), true
}

func (stub *holdsStub) release() {
	stub.mu.Lock()
	stub.released = true
	stub.mu.Unlock()
}

// A work order that planned early publishes its plan and waits, without
// implementing, until the work it builds on has merged; the wait survives a
// restart, and once released its lead first brings main in and the reviewer
// hears that main was merged in (ADR-037).
func TestAnEarlyPlanHoldsBeforeImplementingAndResumesAfterARestart(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	storedProject, err := database.NewProjectStore(db).UpdateAutonomyPolicy(
		t.Context(), storedProject.ID, project.AutonomyPolicyRunToCompletion,
	)
	if err != nil {
		t.Fatalf("enable run-to-completion: %v", err)
	}
	runID := "run_early_plan"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationReviewAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationCorrectionAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 1,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"Published the corrected implementation and added the missing failure-path test.",
	)
	addCompletedLaterImplementationReviewAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 2,
		workerhttp.DispositionSucceeded,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 12,
		"The correction addresses every finding and passes the relevant tests.",
	)
	addCompletedImplementationReadinessAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 2,
		workerhttp.DispositionSucceeded,
		"I agree with the reviewer; this exact commit is ready to merge.",
	)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 28, 11, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_early", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "early-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_early",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	holds := &holdsStub{}
	newStarter := func(ctx context.Context) *RemoteLeadStarter {
		starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
			Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
			Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
			Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
			Validation: validation.NewService(database.NewValidationStore(db)),
			Holds:      holds, Lifetime: ctx,
		})
		if err != nil {
			t.Fatalf("create starter: %v", err)
		}
		return starter
	}
	first, stop := context.WithCancel(t.Context())
	starter := newStarter(first)
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor := func(what string, done func(execution.Run, feature.Feature) bool) execution.Run {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			run, err := executions.GetRun(t.Context(), runID)
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			current, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
			if err != nil {
				t.Fatalf("load feature: %v", err)
			}
			if done(run, current) {
				return run
			}
			time.Sleep(5 * time.Millisecond)
		}
		run, _ := executions.GetRun(t.Context(), runID)
		t.Fatalf("%s: run is %+v", what, run)
		return execution.Run{}
	}
	held := waitFor("hold with the plan published", func(run execution.Run, current feature.Feature) bool {
		return current.State == feature.StatePlanning && run.Status == execution.RunStatusWaitingForUser &&
			execution.HeldBeforeImplementation(run.Reason)
	})
	if held.WaitKind != execution.RunWaitKindPhaseCheckpoint ||
		held.Reason != "Planned; implements once “Lay the foundation” has merged." {
		t.Fatalf("held run = %+v", held)
	}
	implementationTurns := func() []workerhttp.PutAttemptRequest {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		var turns []workerhttp.PutAttemptRequest
		for _, request := range stub.putRequests {
			if request.OutputContract == workerhttp.OutputContractImplementationLead {
				turns = append(turns, request)
			}
		}
		return turns
	}
	if turns := implementationTurns(); len(turns) != 0 || workspaceStub.publishCalls != 1 {
		t.Fatalf("implemented while held: %d turns, %d plan publications", len(turns), workspaceStub.publishCalls)
	}
	// Looking again while it still holds changes nothing.
	if err := starter.ReleaseImplementation(t.Context(), runID); err != nil {
		t.Fatalf("look again while held: %v", err)
	}
	if turns := implementationTurns(); len(turns) != 0 {
		t.Fatalf("implemented while held: %d turns", len(turns))
	}

	// The coordinator restarts; meanwhile the foundation merged.
	stop()
	holds.release()
	restarted := newStarter(t.Context())
	if err := restarted.ReleaseImplementation(t.Context(), runID); err != nil {
		t.Fatalf("release after the restart: %v", err)
	}
	waitFor("reach the merge gate", func(run execution.Run, current feature.Feature) bool {
		return current.State == feature.StateReadyToMerge && run.Status == execution.RunStatusWaitingForUser
	})
	turns := implementationTurns()
	if len(turns) == 0 || !strings.HasPrefix(turns[0].Instructions, heldImplementationInstructions) ||
		!strings.Contains(turns[0].Instructions, "merge (never rebase) its default branch into the feature branch") {
		t.Fatalf("the first implementation turn does not bring main in first: %+v", turns)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	reviewed := false
	for _, request := range stub.putRequests {
		if request.OutputContract == workerhttp.OutputContractImplementationReview {
			reviewed = true
			if !strings.Contains(request.Instructions, "brought main in before implementing") {
				t.Fatalf("the code review does not hear main was merged in: %q", request.Instructions)
			}
		}
	}
	if !reviewed {
		t.Fatal("no code review ran")
	}
}
