package project

import (
	"errors"
	"testing"
)

func TestDialogueLimits(t *testing.T) {
	defaults := DefaultDialogueLimits()
	if defaults.PlanningRounds != 6 || defaults.ImplementationReviewRounds != 6 {
		t.Fatalf("unexpected defaults %+v", defaults)
	}
	if err := (DialogueLimits{}).Validate(); err != nil {
		t.Fatalf("zero should mean unlimited: %v", err)
	}
	for _, limits := range []DialogueLimits{
		{PlanningRounds: -1, ImplementationReviewRounds: 6},
		{PlanningRounds: 6, ImplementationReviewRounds: -1},
	} {
		if err := limits.Validate(); !errors.Is(err, ErrInvalidDialogueLimits) {
			t.Fatalf("expected invalid limits for %+v, got %v", limits, err)
		}
	}
}
