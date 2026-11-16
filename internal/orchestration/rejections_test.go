package orchestration

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
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
