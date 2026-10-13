package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

const reviewContinueGuidance = "The export test checked the old file name; fix the test and run it again."

// startRefusedReview runs a reviewed work order to its second review, an
// approval that is refused because the reviewer's own acceptance test is
// recorded as failed. The reviewer's next try of that round is scripted to
// approve again, and records the test as passed during its turn.
func startRefusedReview(t *testing.T, runID string) (*baseUpdateFixture, string) {
	t.Helper()
	reviewerID := remoteReviewerSessionID(runID)
	retryID := reviewRetryAttemptID(reviewerID, 1, 2, 2)
	fixture := startBaseUpdateRun(t, runID, true, project.MergePolicyRequireUserApproval, 0,
		func(fixture *baseUpdateFixture) {
			// The first check of review 2 fails, so the work order blocks there
			// before acceptance tests are switched on below.
			fixture.workspace.failReviewVerifyCall = 2
			addReviewRetryAttempt(fixture, retryID, 22, "I fixed the export test; it passes and the commit is ready to merge.")
		})
	fixture.waitBlocked(t, "")

	if _, err := fixture.db.ExecContext(
		t.Context(), `UPDATE runs SET independent_acceptance_tests = 1 WHERE id = ?`, fixture.runID,
	); err != nil {
		t.Fatalf("enable acceptance tests: %v", err)
	}
	tests := featureartifact.AcceptanceTests{
		PlanVersion: 1, TestCommitID: strings.Repeat("9", 40), ImplementationCommitID: reviewedApprovedSHA,
		Tests: []featureartifact.AcceptanceTest{
			{ID: "at-1", Position: 1, Title: "Exports the visible columns", Status: featureartifact.AcceptanceTestFailed, Note: "No file named export.csv."},
		},
	}
	actor := workflow.Actor{Kind: workflow.ActorKindAgent, ID: reviewerID}
	if _, err := fixture.workflow.UpsertAcceptanceTests(t.Context(), fixture.featureID, tests, actor, "seed-failed-acceptance"); err != nil {
		t.Fatalf("seed acceptance tests: %v", err)
	}
	fixture.starter.artifacts = fixture.workflow
	fixture.worker.mu.Lock()
	fixture.worker.onPut = func(reference workerhttp.AttemptReference) {
		if reference.AttemptID != retryID {
			return
		}
		for _, status := range []featureartifact.AcceptanceTestStatus{
			featureartifact.AcceptanceTestRunning, featureartifact.AcceptanceTestPassed,
		} {
			if _, err := fixture.workflow.TransitionAcceptanceTest(
				t.Context(), fixture.featureID, 1, "at-1", status, "", reviewedApprovedSHA, actor,
				"rerun-at-1-"+string(status),
			); err != nil {
				t.Errorf("record %s acceptance result: %v", status, err)
			}
		}
	}
	fixture.worker.mu.Unlock()

	// Re-check checks the same review again, which the failed test refuses.
	if _, _, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "recheck-refused-review", RecoveryDirective{Action: RecoveryActionRecheck},
	); err != nil {
		t.Fatalf("recheck review: %v", err)
	}
	blocked := fixture.waitBlocked(t, "")
	if !strings.Contains(blocked.Reason, "is recorded as failed") {
		t.Fatalf("the approval was not refused for its failed acceptance test: %q", blocked.Reason)
	}
	if fixture.workspace.reviewVerifyCalls != 2 || fixture.reviewRetryRequest(retryID) != nil {
		t.Fatalf("re-check did not just check the same review: workspace=%+v", fixture.workspace)
	}
	return fixture, retryID
}

func addReviewRetryAttempt(fixture *baseUpdateFixture, attemptID string, reviewID int64, summary string) {
	stub := fixture.worker
	addCompletedLaterImplementationReviewAttempt(
		stub, fixture.runID, fixture.project.ID, fixture.featureID, 99,
		workerhttp.DispositionSucceeded, reviewedApprovedSHA, reviewID, summary,
	)
	sessionID := remoteReviewerSessionID(fixture.runID)
	scripted := workerhttp.AttemptReference{SessionID: sessionID, AttemptID: implementationReviewAttemptID(sessionID, 99)}
	retry := workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID}
	initial, terminal, events := stub.initial[scripted], stub.terminal[scripted], stub.events[scripted]
	delete(stub.initial, scripted)
	delete(stub.terminal, scripted)
	delete(stub.events, scripted)
	initial.AttemptReference, terminal.AttemptReference = retry, retry
	for index := range events {
		events[index].AttemptReference = retry
	}
	stub.initial[retry], stub.terminal[retry], stub.events[retry] = initial, terminal, events
}

// reviewRetryRequest returns the turn sent to the reviewer as attemptID.
func (fixture *baseUpdateFixture) reviewRetryRequest(attemptID string) *workerhttp.PutAttemptRequest {
	marker := implementationReviewMarker(attemptID)
	for _, request := range fixture.requests() {
		if strings.Contains(request.Instructions, marker) {
			return &request
		}
	}
	return nil
}

func (fixture *baseUpdateFixture) expectReviewRetryAccepted(t *testing.T, retryID string) *workerhttp.PutAttemptRequest {
	t.Helper()
	fixture.waitAtMergeGate(t)
	request := fixture.reviewRetryRequest(retryID)
	if request == nil {
		t.Fatal("the reviewer was not continued in a new try of its review")
	}
	reviewer, err := fixture.executions.GetSession(t.Context(), remoteReviewerSessionID(fixture.runID))
	if err != nil {
		t.Fatalf("load reviewer: %v", err)
	}
	if request.Mode != workerhttp.AttemptModeResume || request.ProviderSessionID == "" ||
		request.ProviderSessionID != reviewer.ProviderSessionID ||
		request.OutputContract != workerhttp.OutputContractImplementationReview ||
		request.Assignment.Role != workerhttp.RoleReviewer {
		t.Fatalf("the reviewer's new try did not continue its review conversation: %+v", request)
	}
	for _, want := range []string{"is recorded as failed", "acceptance pass <test-id>"} {
		if !strings.Contains(request.Instructions, want) {
			t.Fatalf("the reviewer's new try was not told what failed (%q):\n%s", want, request.Instructions)
		}
	}
	verified := fixture.workspace.reviewVerifyAttempts
	if len(verified) != 3 || verified[2] != retryID || fixture.workspace.prepared.ApprovedCommitID != reviewedApprovedSHA {
		t.Fatalf("the new approval was not the one verified: verified=%v workspace=%+v", verified, fixture.workspace)
	}
	if fixture.workspace.readinessVerifyCalls != 1 {
		t.Fatalf("the lead did not acknowledge the accepted review: %+v", fixture.workspace)
	}
	return request
}

func TestContinueWithGuidanceReachesAReviewerWhoseApprovalWasRefused(t *testing.T) {
	fixture, retryID := startRefusedReview(t, "run_review_guided_continue")
	if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "continue-refused-review",
		RecoveryDirective{Action: RecoveryActionContinue, Message: reviewContinueGuidance},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	request := fixture.expectReviewRetryAccepted(t, retryID)
	if !strings.Contains(request.Instructions, "The user asked you to continue with this guidance: "+reviewContinueGuidance) {
		t.Fatalf("the reviewer's new try did not carry the user's guidance:\n%s", request.Instructions)
	}
}

func TestContinueAloneTellsAReviewerWhyItsApprovalWasRefused(t *testing.T) {
	// Checking the same approval again could never pass, so Continue without
	// guidance gives the reviewer the reason instead.
	fixture, retryID := startRefusedReview(t, "run_review_plain_continue")
	if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "continue-refused-review",
		RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	request := fixture.expectReviewRetryAccepted(t, retryID)
	if strings.Contains(request.Instructions, "The user asked you") {
		t.Fatalf("the reviewer was given guidance the user did not write:\n%s", request.Instructions)
	}
}

func TestContinueAloneChecksAReviewRefusedForAnotherReasonAgain(t *testing.T) {
	fixture := startBaseUpdateRun(t, "run_review_continue_recheck", true, project.MergePolicyRequireUserApproval, 0,
		func(fixture *baseUpdateFixture) {
			fixture.workspace.failReviewVerifyCall = 2
		})
	fixture.waitBlocked(t, "")
	if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "continue-unconfirmed-review",
		RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	fixture.waitAtMergeGate(t)
	reviewerID := remoteReviewerSessionID(fixture.runID)
	verified := fixture.workspace.reviewVerifyAttempts
	if len(verified) != 3 || verified[2] != implementationReviewAttemptID(reviewerID, 2) ||
		fixture.reviewRetryRequest(reviewRetryAttemptID(reviewerID, 1, 2, 2)) != nil {
		t.Fatalf("continue did not check the same review again: verified=%v", verified)
	}
}

func TestReviewRetriesKeepTheirRound(t *testing.T) {
	sessionID := "run_retry:reviewer"
	for _, version := range []int{1, 3} {
		first := implementationReviewAttemptForVersion(sessionID, version, 2)
		second, err := recoverySuccessorAttemptID(sessionID, first)
		if err != nil || second != reviewRetryAttemptID(sessionID, version, 2, 2) {
			t.Fatalf("v%d review successor=%q err=%v", version, second, err)
		}
		third, err := recoverySuccessorAttemptID(sessionID, second)
		if err != nil || third != reviewRetryAttemptID(sessionID, version, 2, 3) {
			t.Fatalf("v%d second review successor=%q err=%v", version, third, err)
		}
		gotVersion, round, try, ok := workflowAttemptCoordinates(sessionID, "review", third)
		if !ok || gotVersion != version || round != 2 || try != 3 {
			t.Fatalf("v%d retry coordinates=%d,%d,%d,%t", version, gotVersion, round, try, ok)
		}
	}
	for _, invalid := range []string{
		sessionID + ":review:2:1", sessionID + ":review:2:x", sessionID + ":review:v1:2",
		sessionID + ":review:2:2:2", sessionID + ":correction:2:2",
	} {
		kind := "review"
		if strings.Contains(invalid, ":correction:") {
			kind = "correction"
		}
		if _, _, ok := workflowAttemptVersionAndTurn(sessionID, kind, invalid); ok {
			t.Fatalf("%q parsed as a %s attempt", invalid, kind)
		}
	}
}
