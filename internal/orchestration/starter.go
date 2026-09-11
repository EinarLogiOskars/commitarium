package orchestration

import (
	"context"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type AssignmentFactory func() Assignment

// Starter supplies a fresh set of worker adapters to every run. This matters
// for both scripted workers, whose queues are consumed, and future real worker
// adapters, whose process/session ownership must not leak between runs.
type Starter struct {
	runner            *Runner
	assignmentFactory AssignmentFactory
}

func (s *Starter) Recover(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	recoveryPolicy project.RecoveryPolicy,
) error {
	goal := storedFeature.Title
	if storedFeature.Description != "" {
		goal += ": " + storedFeature.Description
	}
	return s.runner.Recover(ctx, RunRequest{
		ID: run.ID, FeatureID: run.FeatureID, Goal: goal,
		Assignment:        s.assignmentFactory(),
		MaxPlanningRounds: run.PlanningRoundLimit,
		MaxReviewRounds:   run.ImplementationReviewRoundLimit,
		AgentProviders:    run.AgentProviders,
		MergePolicy:       run.MergePolicy,
		RecoveryPolicy:    recoveryPolicy,
		WorkflowPhase:     storedFeature.State,
	})
}

func NewStarter(
	runner *Runner,
	assignmentFactory AssignmentFactory,
) *Starter {
	return &Starter{
		runner: runner, assignmentFactory: assignmentFactory,
	}
}

func (s *Starter) Start(
	ctx context.Context,
	runID string,
	projectID string,
	featureID string,
	goal string,
	dialogueLimits project.DialogueLimits,
	agentProviders project.AgentProviders,
	mergePolicy project.MergePolicy,
) (execution.Run, bool, error) {
	var err error
	mergePolicy, err = project.NormalizeMergePolicy(mergePolicy)
	if err != nil {
		return execution.Run{}, false, err
	}
	_ = projectID // Simulated assignments do not resolve a project workspace.
	return s.runner.Start(ctx, RunRequest{
		ID: runID, FeatureID: featureID, Goal: goal,
		Assignment:        s.assignmentFactory(),
		MaxPlanningRounds: dialogueLimits.PlanningRounds,
		MaxReviewRounds:   dialogueLimits.ImplementationReviewRounds,
		AgentProviders:    agentProviders,
		MergePolicy:       mergePolicy,
	})
}
