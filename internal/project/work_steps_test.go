package project

import (
	"errors"
	"testing"
)

func TestWorkStepsRequireAPlanForAReviewer(t *testing.T) {
	for _, valid := range []WorkSteps{{}, {SkipReviewer: true}, {SkipPlan: true, SkipReviewer: true}} {
		if err := valid.Validate(); err != nil {
			t.Errorf("%+v rejected: %v", valid, err)
		}
	}
	if err := (WorkSteps{SkipPlan: true}).Validate(); !errors.Is(err, ErrInvalidWorkSteps) {
		t.Fatalf("reviewer without a plan error = %v", err)
	}
}
