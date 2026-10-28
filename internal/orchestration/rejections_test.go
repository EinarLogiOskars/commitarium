package orchestration

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// A workspace verification failure the agent can fix is marked with its
// check, so it keeps one fingerprint however its detail reads, and is still
// the workspace failure it was.
func TestRejectPublicationMarksAgentFixableFailures(t *testing.T) {
	for _, test := range []struct {
		cause error
		kind  execution.BlockerKind
	}{
		{workspace.ErrAuditCommentMissing, execution.BlockerAgent},
		{workspace.ErrAuditCommentInvalid, execution.BlockerAgent},
		{workspace.ErrReviewNotAsReported, execution.BlockerAgent},
		{workspace.ErrReportedPullRequestMismatch, execution.BlockerAgent},
		{workspace.ErrNoNewCommit, execution.BlockerAgent},
		{workspace.ErrReportedCommitNotPushed, execution.BlockerCheck},
		{workspace.ErrCheckoutNotAsReported, execution.BlockerCheck},
	} {
		first := fmt.Errorf("verify lead publication before review: %w",
			rejectPublication(fmt.Errorf("%w: found one thing", test.cause)))
		second := fmt.Errorf("verify lead publication before review: %w",
			rejectPublication(fmt.Errorf("%w: found another", test.cause)))
		if !errors.Is(first, test.cause) {
			t.Errorf("%v no longer matches %v", first, test.cause)
		}
		if kind, check := classifyCause(first); kind != test.kind || check != "verify lead publication before review" {
			t.Errorf("classifyCause(%v) = %s, %q", first, kind, check)
		}
		if blockerFingerprintDetail(first) != blockerFingerprintDetail(second) ||
			blockerFingerprintDetail(first) == first.Error() {
			t.Errorf("fingerprint of %v follows its detail", test.cause)
		}
	}
	// Coordinator-side conflicts stay unmarked: the agent cannot fix them.
	for _, cause := range []error{workspace.ErrPullRequestConflict, workspace.ErrCheckoutConflict, workspace.ErrConflict} {
		var rejection *turnRejection
		if errors.As(rejectPublication(cause), &rejection) {
			t.Errorf("%v was marked agent-fixable", cause)
		}
	}
}

// A review refused for its acceptance results keeps one fingerprint whichever
// test it names, and says how to put it right.
func TestAcceptanceRefusalsSayWhatToFix(t *testing.T) {
	tests := func(commit string, status featureartifact.AcceptanceTestStatus, id string) featureartifact.AcceptanceTests {
		return featureartifact.AcceptanceTests{
			PlanVersion: 1, TestCommitID: "1111111111111111111111111111111111111111", ImplementationCommitID: commit,
			Tests: []featureartifact.AcceptanceTest{{ID: id, Position: 1, Title: "t", Status: status}},
		}
	}
	const reviewed, other = "2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333"
	first := validateAcceptanceReviewResults(tests(reviewed, featureartifact.AcceptanceTestPending, "one"), 1, reviewed, workerhttp.DispositionSucceeded)
	second := validateAcceptanceReviewResults(tests(reviewed, featureartifact.AcceptanceTestRunning, "two"), 1, reviewed, workerhttp.DispositionSucceeded)
	if first == nil || second == nil || blockerFingerprintDetail(first) != blockerFingerprintDetail(second) {
		t.Fatalf("incomplete results: %v, %v", first, second)
	}
	if kind, _ := classifyCause(first); kind != execution.BlockerAgent {
		t.Fatalf("incomplete results kind = %s", kind)
	}
	moved := validateAcceptanceReviewResults(tests(other, featureartifact.AcceptanceTestPassed, "one"), 1, reviewed, workerhttp.DispositionSucceeded)
	if moved == nil || !errors.Is(moved, errAcceptanceResultsCommit) ||
		!strings.Contains(moved.Error(), "review commit "+other) {
		t.Fatalf("results of another commit: %v", moved)
	}
}
