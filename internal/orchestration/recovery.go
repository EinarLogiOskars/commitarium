package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type RecoveryDiscovery interface {
	RecoverableRuns(ctx context.Context) ([]execution.Run, error)
	ActiveSessionsForRun(ctx context.Context, runID string) ([]execution.Session, error)
	TransitionRun(
		ctx context.Context,
		id string,
		expected execution.RunStatus,
		status execution.RunStatus,
		reason string,
	) (execution.Run, error)
	RecordSessionEventWithID(
		ctx context.Context,
		id string,
		sessionID string,
		event worker.Event,
	) (execution.Event, error)
}

type RecoveryFeatureFinder interface {
	GetByID(ctx context.Context, id string) (feature.Feature, error)
}

type RecoveryProjectFinder interface {
	GetByID(ctx context.Context, id string) (project.Project, error)
}

type RunRecoverer interface {
	Recover(
		ctx context.Context,
		run execution.Run,
		storedFeature feature.Feature,
		recoveryPolicy project.RecoveryPolicy,
	) error
}

// Recoverer discovers work that cannot have a live in-process owner after a
// coordinator restart. It admits each durable run once; the Runner performs
// the provider reconciliation and approval gate asynchronously.
type Recoverer struct {
	executions RecoveryDiscovery
	features   RecoveryFeatureFinder
	projects   RecoveryProjectFinder
	runs       RunRecoverer
}

func NewRecoverer(
	executions RecoveryDiscovery,
	features RecoveryFeatureFinder,
	projects RecoveryProjectFinder,
	runs RunRecoverer,
) *Recoverer {
	return &Recoverer{
		executions: executions, features: features, projects: projects, runs: runs,
	}
}

func (r *Recoverer) RecoverAll(ctx context.Context) (int, error) {
	runs, err := r.executions.RecoverableRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("discover interrupted runs: %w", err)
	}
	recovered := 0
	var recoveryErrors []error
	for _, run := range runs {
		activeSessions, err := r.executions.ActiveSessionsForRun(ctx, run.ID)
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("inspect run %q: %w", run.ID, err))
			continue
		}
		if len(activeSessions) > 1 {
			cause := fmt.Errorf(
				"run %q has %d active sessions; expected at most one",
				run.ID,
				len(activeSessions),
			)
			if err := r.blockContradictoryRun(ctx, run, activeSessions, cause.Error()); err != nil {
				cause = errors.Join(cause, err)
			}
			recoveryErrors = append(recoveryErrors, cause)
			continue
		}
		storedFeature, err := r.features.GetByID(ctx, run.FeatureID)
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("load feature for run %q: %w", run.ID, err))
			continue
		}
		storedProject, err := r.projects.GetByID(ctx, storedFeature.ProjectID)
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("load project for run %q: %w", run.ID, err))
			continue
		}
		if err := r.runs.Recover(ctx, run, storedFeature, storedProject.RecoveryPolicy); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("recover run %q: %w", run.ID, err))
			continue
		}
		recovered++
	}
	return recovered, errors.Join(recoveryErrors...)
}

func (r *Recoverer) blockContradictoryRun(
	ctx context.Context,
	run execution.Run,
	sessions []execution.Session,
	reason string,
) error {
	var blockErrors []error
	for _, session := range sessions {
		if _, err := r.executions.RecordSessionEventWithID(
			ctx,
			session.ID+":recovery:contradictory-active-sessions",
			session.ID,
			worker.Event{Type: worker.EventRecoveryAssessment, Text: reason},
		); err != nil {
			blockErrors = append(blockErrors, err)
		}
	}
	if run.Status == execution.RunStatusRunning {
		if _, err := r.executions.TransitionRun(
			ctx,
			run.ID,
			execution.RunStatusRunning,
			execution.RunStatusWaitingForUser,
			reason,
		); err != nil {
			blockErrors = append(blockErrors, err)
		}
	}
	return errors.Join(blockErrors...)
}
