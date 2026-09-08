package orchestration

import (
	"context"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

type AssignmentFactory func() Assignment

// Starter supplies a fresh set of worker adapters to every run. This matters
// for both scripted workers, whose queues are consumed, and future real worker
// adapters, whose process/session ownership must not leak between runs.
type Starter struct {
	runner            *Runner
	assignmentFactory AssignmentFactory
	maxPlanningRounds int
	maxReviewRounds   int
}

func NewStarter(
	runner *Runner,
	assignmentFactory AssignmentFactory,
	maxPlanningRounds int,
	maxReviewRounds int,
) *Starter {
	return &Starter{
		runner: runner, assignmentFactory: assignmentFactory,
		maxPlanningRounds: maxPlanningRounds, maxReviewRounds: maxReviewRounds,
	}
}

func (s *Starter) Start(
	ctx context.Context,
	runID string,
	featureID string,
	goal string,
) (execution.Run, bool, error) {
	return s.runner.Start(ctx, RunRequest{
		ID: runID, FeatureID: featureID, Goal: goal,
		Assignment:        s.assignmentFactory(),
		MaxPlanningRounds: s.maxPlanningRounds,
		MaxReviewRounds:   s.maxReviewRounds,
	})
}
