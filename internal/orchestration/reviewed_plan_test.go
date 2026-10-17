package orchestration

import (
	"strings"
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

// TestRemoteLeadReviewedPlanSkipsCodeReview runs a "Reviewed plan" work order
// (ADR-031) from Start to merge: the lead and the reviewer agree a plan as in
// a full run, the lead implements, and the verified publication goes straight
// to the merge gate without acceptance tests, code review, or readiness.
func TestRemoteLeadReviewedPlanSkipsCodeReview(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	storedProject, err := database.NewProjectStore(db).UpdateAutonomyPolicy(
		t.Context(), storedProject.ID, project.AutonomyPolicyRunToCompletion,
	)
	if err != nil {
		t.Fatalf("enable run-to-completion: %v", err)
	}
	runID := "run_reviewed_plan"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 17, 11, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_reviewed_plan", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "reviewed-plan-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_reviewed_plan",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	features := database.NewFeatureStore(db)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: features, Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Validation: validation.NewService(database.NewValidationStore(db)),
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	chosen, err := features.GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("load ready feature: %v", err)
	}
	chosen.Steps = project.WorkSteps{SkipCodeReview: true}
	if err := features.UpdateReadySettings(t.Context(), chosen); err != nil {
		t.Fatalf("choose steps: %v", err)
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var gated execution.Run
	var current feature.Feature
	for time.Now().Before(deadline) {
		gated, err = executions.GetRun(t.Context(), runID)
		if err != nil {
			t.Fatalf("load run: %v", err)
		}
		current, err = features.GetByID(t.Context(), storedFeature.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if current.State == feature.StateReadyToMerge && gated.Status == execution.RunStatusWaitingForUser {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gated.Status != execution.RunStatusWaitingForUser || gated.WaitKind != execution.RunWaitKindMergeGate ||
		gated.Paused || current.State != feature.StateReadyToMerge {
		t.Fatalf("reviewed-plan run did not reach the merge gate: run %+v, feature state %s", gated, current.State)
	}
	if !gated.Steps.SkipCodeReview || gated.Steps.SkipReviewer || gated.Steps.SkipPlan {
		t.Fatalf("run did not keep the reviewed-plan steps: %+v", gated.Steps)
	}

	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	var reviewerPlanningTurns, planApprovals, implementationTurns int
	for _, request := range requests {
		switch request.OutputContract {
		case workerhttp.OutputContractImplementationReview,
			workerhttp.OutputContractAcceptanceTests,
			workerhttp.OutputContractImplementationReadiness:
			t.Fatalf("a reviewed-plan run sent a %s turn", request.OutputContract)
		case workerhttp.OutputContractPlanApproval:
			planApprovals++
		case workerhttp.OutputContractImplementationLead:
			implementationTurns++
		}
		if request.Assignment.Role == workerhttp.RoleReviewer &&
			request.OutputContract != workerhttp.OutputContractPlanApproval {
			reviewerPlanningTurns++
		}
	}
	if reviewerPlanningTurns == 0 || planApprovals != 1 || implementationTurns != 1 {
		t.Fatalf(
			"expected reviewer planning, one plan confirmation and one implementation; got %d reviewer planning turns, %d plan approvals, %d implementation turns",
			reviewerPlanningTurns, planApprovals, implementationTurns,
		)
	}
	// Lead plan, reviewer, lead, reviewer, lead submits, plan approval,
	// implementation.
	if len(requests) != 7 {
		t.Fatalf("expected 7 turns, got %d", len(requests))
	}
	if workspaceStub.publishCalls != 1 || workspaceStub.publicationVerifyCalls != 1 ||
		workspaceStub.reviewVerifyCalls != 0 || workspaceStub.responseVerifyCalls != 0 ||
		workspaceStub.readinessVerifyCalls != 0 || workspaceStub.mergeCalls != 0 {
		t.Fatalf("unexpected publication/review calls: %+v", workspaceStub)
	}

	if _, _, err := starter.Merge(t.Context(), runID, "merge-reviewed-plan"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if workspaceStub.mergeCalls != 1 || len(workspaceStub.mergeApprovalNotes) != 1 ||
		!strings.Contains(workspaceStub.mergeApprovalNotes[0], "no code review") {
		t.Fatalf("merge did not carry the no-code-review approval note: %+v", workspaceStub.mergeApprovalNotes)
	}
}
