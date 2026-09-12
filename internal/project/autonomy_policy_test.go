package project

import (
	"errors"
	"testing"
)

func TestNormalizeAutonomyPolicy(t *testing.T) {
	tests := []struct {
		name string
		in   AutonomyPolicy
		want AutonomyPolicy
		err  error
	}{
		{name: "default", want: AutonomyPolicyReviewEachPhase},
		{name: "review each phase", in: AutonomyPolicyReviewEachPhase, want: AutonomyPolicyReviewEachPhase},
		{name: "run to completion", in: AutonomyPolicyRunToCompletion, want: AutonomyPolicyRunToCompletion},
		{name: "invalid", in: "surprise", err: ErrInvalidAutonomyPolicy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeAutonomyPolicy(test.in)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if got != test.want {
				t.Fatalf("policy = %q, want %q", got, test.want)
			}
		})
	}
}
