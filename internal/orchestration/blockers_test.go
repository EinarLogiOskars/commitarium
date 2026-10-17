package orchestration

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// A blocker names what failed, so recovery can match the move to it
// (ADR-032).
func TestClassifyCauseNamesTheFailure(t *testing.T) {
	for _, test := range []struct {
		cause error
		kind  execution.BlockerKind
		check string
	}{
		{fmt.Errorf("verify lead publication before review: verify implementation audit comment: %w", workspace.ErrAuditCommentMissing),
			execution.BlockerAgent, "verify lead publication before review"},
		{fmt.Errorf("verify lead publication before review: %w", workspace.ErrPullRequestConflict),
			execution.BlockerCheck, "verify lead publication before review"},
		{fmt.Errorf("prepare: %w", workspace.ErrCheckoutConflict), execution.BlockerCheck, "prepare"},
		{errors.New("lead completed implementation without structured publication facts"), execution.BlockerAgent,
			"lead completed implementation without structured publication facts"},
		{errors.New("something new"), execution.BlockerOther, "something new"},
	} {
		kind, check := classifyCause(test.cause)
		if kind != test.kind || check != test.check {
			t.Errorf("classifyCause(%v) = %s, %q; want %s, %q", test.cause, kind, check, test.kind, test.check)
		}
	}
}

func TestBlockerFingerprintsRecogniseRepeats(t *testing.T) {
	first := execution.BlockerFingerprint(execution.BlockerCheck, "verify", "audit comment missing")
	if first != execution.BlockerFingerprint(execution.BlockerCheck, "verify", " audit comment missing ") ||
		first == execution.BlockerFingerprint(execution.BlockerAgent, "verify", "audit comment missing") {
		t.Fatal("fingerprints")
	}
}
