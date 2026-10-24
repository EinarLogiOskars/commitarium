package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// The first code review is one verification pass (ADR-031): suite and merge
// first, reproductions for risky spots, defects only, and what it could not
// run named as not verified.
func TestTheFirstCodeReviewIsOneVerificationPass(t *testing.T) {
	instructions := implementationReviewInstructions(
		workspace.Workspace{RepositoryOwner: "o", RepositoryName: "r", BaseBranch: "main", Branch: "feature"},
		"Done.", "0123456789abcdef0123456789abcdef01234567", "att_review", "", false,
	)
	for _, want := range []string{
		"one verification pass", "full test suite", "git merge-tree", "throwaway reproduction",
		"only for defects and missed requirements", "not verified",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("review instructions lack %q", want)
		}
	}
}

// A finding the lead declines, or rules out of scope, needs no code change:
// the lead answers each finding by kind and keeps the reviewed commit when
// nothing changes, and the reviewer then judges the answers rather than a
// diff (Gambario's manual screen-reader check).
func TestAReviewResponseMayChangeNothing(t *testing.T) {
	prepared := workspace.Workspace{RepositoryOwner: "o", RepositoryName: "r", BaseBranch: "main", Branch: "feature"}
	reviewed := "0123456789abcdef0123456789abcdef01234567"
	correction := implementationCorrectionInstructions(prepared, "F1 needs a manual screen-reader check.", reviewed, 11, "att_correction")
	for _, want := range []string{
		"declined", "out of scope", "make no commit (never one just to have a new commit)",
		"the reviewed commit when you changed nothing", "finding_answers",
	} {
		if !strings.Contains(correction, want) {
			t.Errorf("correction instructions lack %q", want)
		}
	}
	rereview := implementationReviewInstructions(prepared, "F1 out of scope.", reviewed, "att_review_2", reviewed, false)
	if !strings.Contains(rereview, "The lead changed nothing") || strings.Contains(rereview, "git diff "+reviewed) {
		t.Fatalf("re-review of an unchanged response:\n%s", rereview)
	}
}
