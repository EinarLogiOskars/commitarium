package project

import "errors"

// AutonomyPolicy controls whether a real-provider run stops at the safe
// boundaries between planning and implementation phases.
type AutonomyPolicy string

const (
	AutonomyPolicyReviewEachPhase AutonomyPolicy = "review_each_phase"
	AutonomyPolicyRunToCompletion AutonomyPolicy = "run_to_completion"
)

var ErrInvalidAutonomyPolicy = errors.New("invalid project autonomy policy")

func DefaultAutonomyPolicy() AutonomyPolicy {
	return AutonomyPolicyReviewEachPhase
}

func NormalizeAutonomyPolicy(policy AutonomyPolicy) (AutonomyPolicy, error) {
	if policy == "" {
		return DefaultAutonomyPolicy(), nil
	}
	switch policy {
	case AutonomyPolicyReviewEachPhase, AutonomyPolicyRunToCompletion:
		return policy, nil
	default:
		return "", ErrInvalidAutonomyPolicy
	}
}
