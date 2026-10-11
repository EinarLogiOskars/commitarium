package workorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

// ErrNotStarted means the work order has no run to set aside.
var ErrNotStarted = errors.New("work order has not been started")

// ResetStore claims and finishes returning a started work order to Ready.
type ResetStore interface {
	// BeginReturnToReady loads a started, unmerged work order and its
	// workspace. It returns ErrActive while an agent is still working and
	// ErrNotStarted when the work order is not started (a Ready one is
	// returned with no error, so a repeated request finishes).
	BeginReturnToReady(ctx context.Context, projectID, featureID string) (Deletion, error)
	// FinishReturnToReady forgets the work order's workspace and its goal
	// acceptance, so the next Start begins from the brief on a fresh branch.
	FinishReturnToReady(ctx context.Context, projectID, featureID string) error
}

// FeatureTransitioner records the work order's move back to Ready.
type FeatureTransitioner interface {
	TransitionFeature(ctx context.Context, featureID string, state feature.State, actor workflow.Actor, idempotencyKey string) (workflow.Event, error)
}

// WithReset enables returning started work orders to Ready.
func (service *Service) WithReset(store ResetStore, transitions FeatureTransitioner) *Service {
	service.reset, service.transitions = store, transitions
	return service
}

// ReturnToReady sets a started, unmerged work order back to Ready (ADR-017):
// its runs stop, its pull request is closed and its branch and checkouts are
// removed, and its handoff brief is kept. The runs stay in its history. The
// request key makes the steps safe to repeat after an interruption.
func (service *Service) ReturnToReady(
	ctx context.Context,
	projectID string,
	featureID string,
	requestKey string,
) (feature.Feature, error) {
	if service.reset == nil || service.transitions == nil || service.runs == nil {
		return feature.Feature{}, fmt.Errorf("%w: returning to Ready is unavailable", ErrUnavailable)
	}
	stop := service.runs.StopWorkOrderRuns
	if restarter, ok := service.runs.(interface {
		StopWorkOrderRunsForReady(context.Context, string, string) error
	}); ok {
		stop = restarter.StopWorkOrderRunsForReady
	}
	if err := stop(ctx, featureID, requestKey); err != nil {
		return feature.Feature{}, fmt.Errorf("%w: stop active agent work: %v", ErrUnavailable, err)
	}
	claimed, err := service.reset.BeginReturnToReady(ctx, projectID, featureID)
	if err != nil {
		return feature.Feature{}, err
	}
	if claimed.Workspace != nil {
		if err := service.removeArtifacts(ctx, claimed.Feature, *claimed.Workspace); err != nil {
			return feature.Feature{}, err
		}
	}
	if err := service.reset.FinishReturnToReady(ctx, projectID, featureID); err != nil {
		return feature.Feature{}, err
	}
	if claimed.Feature.State != feature.StateReady {
		if _, err := service.transitions.TransitionFeature(
			ctx, featureID, feature.StateReady,
			workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, requestKey+":ready",
		); err != nil {
			return feature.Feature{}, err
		}
		claimed.Feature.State = feature.StateReady
	}
	claimed.Feature.AcceptedGoal, claimed.Feature.GoalAcceptedAt = "", nil
	return claimed.Feature, nil
}
