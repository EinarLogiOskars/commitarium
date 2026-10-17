package project

import (
	"errors"
	"fmt"
)

// ErrInvalidWorkSteps rejects a combination of optional work-order steps.
var ErrInvalidWorkSteps = errors.New("invalid work-order steps")

// WorkSteps records which optional steps a work order skips (ADR-018). The
// zero value is the full flow, so records made before steps existed keep it.
// Clarification is not recorded: it only decides whether Start accepts a
// draft.
type WorkSteps struct {
	// SkipPlan lets the lead plan internally instead of writing a documented,
	// commit-ordered plan.
	SkipPlan bool
	// SkipReviewer drops plan review, independent acceptance tests, code
	// review, and the readiness acknowledgement.
	SkipReviewer bool
	// SkipCodeReview keeps plan review but drops what follows the
	// implementation: independent acceptance tests, code review, and the
	// readiness acknowledgement (ADR-031). It needs no reviewer to skip.
	SkipCodeReview bool
}

// CodeReview reports whether a reviewer reviews the implementation.
func (steps WorkSteps) CodeReview() bool {
	return !steps.SkipReviewer && !steps.SkipCodeReview
}

// Validate rejects a reviewer without a documented plan: plan review is part
// of review, and review is all or nothing.
func (steps WorkSteps) Validate() error {
	if steps.SkipPlan && !steps.SkipReviewer {
		return fmt.Errorf("%w: a reviewer requires a documented plan", ErrInvalidWorkSteps)
	}
	return nil
}
