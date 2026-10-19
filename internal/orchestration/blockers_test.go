package orchestration

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
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
		{fmt.Errorf("%w: step 2 \"edit\" is in_progress", errImplementationChecklist), execution.BlockerAgent,
			errImplementationChecklist.Error()},
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

// A checklist mismatch is one failure however its detail changes, so a
// recovery already tried on it is recognised.
func TestChecklistBlockerFingerprintIgnoresItsDetail(t *testing.T) {
	first := fmt.Errorf("%w: step 2 \"edit\" is in_progress", errImplementationChecklist)
	second := fmt.Errorf("%w: step 3 \"delete\" is pending", errImplementationChecklist)
	if blockerFingerprintDetail(first) != blockerFingerprintDetail(second) ||
		blockerFingerprintDetail(first) != errImplementationChecklist.Error() {
		t.Fatalf("fingerprint details differ: %q, %q", blockerFingerprintDetail(first), blockerFingerprintDetail(second))
	}
	if other := errors.New("something new"); blockerFingerprintDetail(other) != other.Error() {
		t.Fatalf("other cause fingerprint detail = %q", blockerFingerprintDetail(other))
	}
}

// The checklist error names each unfinished step and the exact command
// that records it, so the lead can fix it in one turn.
func TestCheckImplementationChecklistSaysWhatToFix(t *testing.T) {
	const first, published = "1111111111111111111111111111111111111111", "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	step := func(position int, id string, status featureartifact.StepStatus, commit string) featureartifact.ImplementationPlanStep {
		return featureartifact.ImplementationPlanStep{ID: id, Position: position, Status: status, CommitID: commit}
	}
	for _, test := range []struct {
		name      string
		checklist featureartifact.ImplementationPlan
		want      []string
	}{
		{"plan version", featureartifact.ImplementationPlan{PlanVersion: 1, Steps: []featureartifact.ImplementationPlanStep{
			step(1, "contract", featureartifact.StepCompleted, published),
		}}, []string{"plan version 1 but the agreed plan is version 2"}},
		{"no steps", featureartifact.ImplementationPlan{PlanVersion: 2}, []string{"no steps"}},
		{"unfinished steps", featureartifact.ImplementationPlan{PlanVersion: 2, Steps: []featureartifact.ImplementationPlanStep{
			step(1, "contract", featureartifact.StepCompleted, first),
			step(2, "inline-item-editor", featureartifact.StepInProgress, ""),
			step(3, "delete-item", featureartifact.StepPending, ""),
		}}, []string{
			`step 2 "inline-item-editor" is in_progress: record its commit with ` +
				`'commitarium-artifact plan complete inline-item-editor <lowercase-commit-id>'`,
			`step 3 "delete-item" is pending: run 'commitarium-artifact plan start delete-item', then ` +
				`'commitarium-artifact plan complete delete-item abcdef0123456789abcdef0123456789abcdef01'`,
		}},
		{"last commit", featureartifact.ImplementationPlan{PlanVersion: 2, Steps: []featureartifact.ImplementationPlanStep{
			step(1, "contract", featureartifact.StepCompleted, first),
		}}, []string{`the last step "contract" is recorded at commit ` + first + ` but the published commit is ` + published}},
	} {
		err := checkImplementationChecklist(test.checklist, 2, published)
		if !errors.Is(err, errImplementationChecklist) {
			t.Fatalf("%s: error %v does not wrap the checklist error", test.name, err)
		}
		for _, want := range test.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not contain %q", test.name, err, want)
			}
		}
	}
	matching := featureartifact.ImplementationPlan{PlanVersion: 2, Steps: []featureartifact.ImplementationPlanStep{
		step(1, "contract", featureartifact.StepCompleted, published),
	}}
	if err := checkImplementationChecklist(matching, 2, published); err != nil {
		t.Fatalf("matching checklist: %v", err)
	}
}
