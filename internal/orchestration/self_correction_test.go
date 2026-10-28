package orchestration

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// errTestReviewMarker stands in for a check the reviewer can put right.
var errTestReviewMarker = agentRejection("the review comment does not carry the review marker")

func reviewMarkerRefusal(found string) error {
	return fmt.Errorf("verify review: %w: expected the marker on the review comment, found %s; "+
		"post the review again with the marker", errTestReviewMarker, found)
}

// startSelfCorrectedReview runs a reviewed work order whose second review is
// refused by the calls in refusals; tries scripts that many new tries of it.
func startSelfCorrectedReview(t *testing.T, runID string, refusals map[int]error, tries int) *baseUpdateFixture {
	t.Helper()
	reviewerID := remoteReviewerSessionID(runID)
	return startBaseUpdateRun(t, runID, true, project.MergePolicyRequireUserApproval, 0,
		func(fixture *baseUpdateFixture) {
			fixture.workspace.reviewRefusals = refusals
			for try := 2; try < 2+tries; try++ {
				addReviewRetryAttempt(fixture, reviewRetryAttemptID(reviewerID, 1, 2, try), int64(20+try),
					"I posted the review again with its marker.")
			}
		})
}

func (fixture *baseUpdateFixture) selfCorrections(t *testing.T) []execution.Recovery {
	t.Helper()
	recoveries, err := fixture.executions.RunRecoveries(t.Context(), fixture.runID)
	if err != nil {
		t.Fatalf("load recoveries: %v", err)
	}
	var corrections []execution.Recovery
	for _, recovery := range recoveries {
		if recovery.How == execution.RecoverySelfCorrection {
			corrections = append(corrections, recovery)
		}
	}
	return corrections
}

func TestARefusedTurnIsHandedBackWithItsReasonAndAcceptedOnTheCorrection(t *testing.T) {
	refusal := reviewMarkerRefusal("no marker")
	fixture := startSelfCorrectedReview(t, "run_self_correct_review", map[int]error{2: refusal}, 1)
	fixture.waitAtMergeGate(t)

	reviewerID := remoteReviewerSessionID(fixture.runID)
	retryID := reviewRetryAttemptID(reviewerID, 1, 2, 2)
	request := fixture.reviewRetryRequest(retryID)
	if request == nil {
		t.Fatal("the refused review was not handed back to the reviewer")
	}
	reviewer, err := fixture.executions.GetSession(t.Context(), reviewerID)
	if err != nil {
		t.Fatalf("load reviewer: %v", err)
	}
	if request.Mode != workerhttp.AttemptModeResume || request.ProviderSessionID != reviewer.ProviderSessionID {
		t.Fatalf("the correction did not continue the reviewer's conversation: %+v", request)
	}
	prompt := selfCorrectionPrompt(fmt.Errorf("verify reviewer publication: %w", refusal))
	for _, want := range []string{
		"Commitarium's correction:\n" + prompt,
		"Commitarium could not accept your last turn: verify reviewer publication: verify review: the review comment does not carry the review marker",
		"post the review again with the marker",
	} {
		if !strings.Contains(request.Instructions, want) {
			t.Fatalf("the correction did not carry %q:\n%s", want, request.Instructions)
		}
	}
	if strings.Contains(request.Instructions, "The user asked you") {
		t.Fatalf("the correction was framed as the user's guidance:\n%s", request.Instructions)
	}
	if verified := fixture.workspace.reviewVerifyAttempts; len(verified) != 3 || verified[2] != retryID {
		t.Fatalf("the corrected review was not the one verified: %v", verified)
	}
	corrections := fixture.selfCorrections(t)
	if len(corrections) != 1 || corrections[0].Guidance != prompt || corrections[0].FromAssistant ||
		corrections[0].Fingerprint != execution.BlockerFingerprint(execution.BlockerAgent, "verify reviewer publication", errTestReviewMarker.Error()) {
		t.Fatalf("the correction was not recorded as a self-correction of that failure: %+v", corrections)
	}
	assertGuidanceShown(t, fixture.executions, reviewerID, "commitarium", prompt)
}

func TestTwoFailedCorrectionsPauseAsABlocker(t *testing.T) {
	fixture := startSelfCorrectedReview(t, "run_self_correct_twice", map[int]error{
		2: reviewMarkerRefusal("no marker"),
		3: reviewMarkerRefusal("a marker for another attempt"),
		4: reviewMarkerRefusal("a marker for a third attempt"),
	}, 2)
	blocked := fixture.waitBlocked(t, "")
	if fixture.workspace.reviewVerifyCalls != 4 {
		t.Fatalf("review checked %d times, want the review and its two corrections", fixture.workspace.reviewVerifyCalls)
	}
	if corrections := fixture.selfCorrections(t); len(corrections) != MaxSelfCorrections {
		t.Fatalf("self-corrections = %+v, want %d", corrections, MaxSelfCorrections)
	}
	for _, want := range []string{
		"The coordinator could not complete the reviewer turn",
		"a marker for a third attempt",
		"Commitarium handed it back to the reviewer 2 times; it came back each time.",
	} {
		if !strings.Contains(blocked.Reason, want) {
			t.Fatalf("blocker reason does not say %q: %q", want, blocked.Reason)
		}
	}
	blocker, found, err := fixture.executions.RunBlocker(t.Context(), fixture.runID)
	if err != nil || !found || blocker.Kind != execution.BlockerAgent {
		t.Fatalf("blocker = %+v found=%t err=%v", blocker, found, err)
	}
	// The assistant may still recover it: the corrections count towards the
	// limits, but do not use them up.
	recoveries, _ := fixture.executions.RunRecoveries(t.Context(), fixture.runID)
	if execution.RecoveryExhausted(blocker, recoveries) {
		t.Fatal("two self-corrections exhausted recovery")
	}
}

func TestTheSameRefusalAfterACorrectionIsLikelyACommitariumProblem(t *testing.T) {
	refusal := reviewMarkerRefusal("no marker")
	fixture := startSelfCorrectedReview(t, "run_self_correct_suspect", map[int]error{2: refusal, 3: refusal}, 2)
	blocked := fixture.waitBlocked(t, "")
	if fixture.workspace.reviewVerifyCalls != 3 || len(fixture.selfCorrections(t)) != 1 {
		t.Fatalf("handed back again after the same refusal: checks=%d corrections=%+v",
			fixture.workspace.reviewVerifyCalls, fixture.selfCorrections(t))
	}
	if !strings.Contains(blocked.Reason, "this is likely a problem in Commitarium, not in the work") {
		t.Fatalf("blocker reason does not name a likely Commitarium problem: %q", blocked.Reason)
	}
	blocker, found, err := fixture.executions.RunBlocker(t.Context(), fixture.runID)
	if err != nil || !found || blocker.Kind != execution.BlockerCommitarium ||
		blocker.Fingerprint != execution.BlockerFingerprint(execution.BlockerAgent, "verify reviewer publication", errTestReviewMarker.Error()) {
		t.Fatalf("blocker = %+v found=%t err=%v", blocker, found, err)
	}
	recoveries, _ := fixture.executions.RunRecoveries(t.Context(), fixture.runID)
	if !execution.RecoveryExhausted(blocker, recoveries) {
		t.Fatal("a likely Commitarium problem is left for the assistant to recover")
	}
}

// Only a check's refusal of what the agent did goes back to it: an
// interrupted turn retries by itself, and a failure on Commitarium's side
// (its store, a worker it cannot reach) is nothing the agent can fix.
func TestOnlyRefusalsTheAgentCanFixAreHandedBack(t *testing.T) {
	fixture := startBaseUpdateRun(t, "run_self_correct_kinds", true, project.MergePolicyRequireUserApproval, 0, nil)
	fixture.waitAtMergeGate(t)
	for _, cause := range []error{
		errInterruptedAttempt,
		fmt.Errorf("observe: %w", workerhttp.ErrRequestFailed),
		fmt.Errorf("verify review: %w", errors.New("database is locked")),
		errors.New("lead completed implementation without structured publication facts"),
		fmt.Errorf("observe: %w: %w", errTestReviewMarker, workerhttp.ErrRequestFailed),
	} {
		if plan := fixture.starter.planSelfCorrection(t.Context(), fixture.runID, cause, "fingerprint"); plan.prompt != "" {
			t.Errorf("%v was handed back to the agent", cause)
		}
	}
	if plan := fixture.starter.planSelfCorrection(
		t.Context(), fixture.runID, reviewMarkerRefusal("no marker"), "fingerprint",
	); plan.prompt == "" {
		t.Error("a refusal the agent can fix was not handed back")
	}
}

func TestRejectionFingerprintsIgnoreTheirWording(t *testing.T) {
	first, second := reviewMarkerRefusal("no marker"), fmt.Errorf("verify review again: %w: reworded", errTestReviewMarker)
	if blockerFingerprintDetail(first) != blockerFingerprintDetail(second) ||
		blockerFingerprintDetail(first) != errTestReviewMarker.Error() {
		t.Fatalf("fingerprint details differ: %q, %q", blockerFingerprintDetail(first), blockerFingerprintDetail(second))
	}
	other := fmt.Errorf("verify review: %w: no marker", agentRejection("another check"))
	if blockerFingerprintDetail(other) == blockerFingerprintDetail(first) {
		t.Fatal("two checks share a fingerprint")
	}
	if kind, _ := classifyCause(fmt.Errorf("verify: %w: detail", stateRejection("the branch is behind"))); kind != execution.BlockerCheck {
		t.Fatalf("a state rejection classified as %s", kind)
	}
}

func TestSelfCorrectionPromptsHideSecretsAndStayShort(t *testing.T) {
	cause := errors.New("push to https://lead:s3cr3t-value@forgejo.local/o/r.git failed with Authorization: token abcdef0123456789; " +
		"token=hunter2 password: swordfish ghp_0123456789abcdefABCDEF at commit 0123456789abcdef0123456789abcdef01234567 " +
		strings.Repeat("é", maxCorrectionReason))
	prompt := selfCorrectionPrompt(cause)
	for _, secret := range []string{"s3cr3t-value", "abcdef0123456789;", "hunter2", "swordfish", "ghp_0123456789abcdefABCDEF"} {
		if strings.Contains(prompt, secret) {
			t.Errorf("prompt leaks %q: %s", secret, prompt)
		}
	}
	if !strings.Contains(prompt, "0123456789abcdef0123456789abcdef01234567") || !strings.Contains(prompt, "[REDACTED]") {
		t.Errorf("prompt lost the commit or redacted nothing: %s", prompt)
	}
	if len(prompt) > maxCorrectionReason+400 || !strings.Contains(prompt, "…") || !strings.HasPrefix(prompt,
		"Commitarium could not accept your last turn: push to https://[REDACTED]@forgejo.local") {
		t.Errorf("prompt not bounded on a rune boundary: %d bytes", len(prompt))
	}
}
