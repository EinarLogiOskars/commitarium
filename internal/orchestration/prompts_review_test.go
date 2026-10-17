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
