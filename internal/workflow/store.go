package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

type FeatureTransition struct {
	EventID        string
	FeatureID      string
	State          feature.State
	Actor          Actor
	OccurredAt     time.Time
	IdempotencyKey string
}

type GoalAcceptance struct {
	EventID        string
	FeatureID      string
	SessionID      string
	Goal           string
	Actor          Actor
	OccurredAt     time.Time
	IdempotencyKey string
}

type Store interface {
	ApplyFeatureTransition(
		ctx context.Context,
		transition FeatureTransition,
	) (Event, error)
	AcceptGoal(ctx context.Context, acceptance GoalAcceptance) (Event, error)
	ListEvents(ctx context.Context, aggregateID string) ([]Event, error)
}

var ErrInvalidTransitionRequest = errors.New("invalid feature transition request")
var ErrInvalidGoalAcceptance = errors.New("invalid goal acceptance")
var ErrGoalAlreadyAccepted = errors.New("feature goal is already accepted")
var ErrGoalAcceptanceNotAllowed = errors.New("goal acceptance is not allowed")
var ErrIdempotencyConflict = errors.New("idempotency key reused for a different command")

func (transition FeatureTransition) Validate() error {
	switch {
	case strings.TrimSpace(transition.EventID) == "":
		return fmt.Errorf("%w: event ID is required", ErrInvalidTransitionRequest)
	case strings.TrimSpace(transition.FeatureID) == "":
		return fmt.Errorf("%w: feature ID is required", ErrInvalidTransitionRequest)
	case !transition.State.IsValid():
		return fmt.Errorf(
			"%w: state %q is not recognized",
			ErrInvalidTransitionRequest,
			transition.State,
		)
	case !transition.Actor.Kind.IsValid():
		return fmt.Errorf(
			"%w: actor kind %q is not recognized",
			ErrInvalidTransitionRequest,
			transition.Actor.Kind,
		)
	case strings.TrimSpace(transition.Actor.ID) == "":
		return fmt.Errorf("%w: actor ID is required", ErrInvalidTransitionRequest)
	case transition.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidTransitionRequest)
	case strings.TrimSpace(transition.IdempotencyKey) == "":
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidTransitionRequest)
	default:
		return nil
	}
}

func (acceptance GoalAcceptance) Validate() error {
	switch {
	case strings.TrimSpace(acceptance.EventID) == "":
		return fmt.Errorf("%w: event ID is required", ErrInvalidGoalAcceptance)
	case strings.TrimSpace(acceptance.FeatureID) == "":
		return fmt.Errorf("%w: feature ID is required", ErrInvalidGoalAcceptance)
	case strings.TrimSpace(acceptance.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidGoalAcceptance)
	case strings.TrimSpace(acceptance.Goal) == "":
		return fmt.Errorf("%w: goal is required", ErrInvalidGoalAcceptance)
	case acceptance.Goal != strings.TrimSpace(acceptance.Goal):
		return fmt.Errorf("%w: goal must be trimmed", ErrInvalidGoalAcceptance)
	case !acceptance.Actor.Kind.IsValid():
		return fmt.Errorf("%w: actor kind is not recognized", ErrInvalidGoalAcceptance)
	case strings.TrimSpace(acceptance.Actor.ID) == "":
		return fmt.Errorf("%w: actor ID is required", ErrInvalidGoalAcceptance)
	case acceptance.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidGoalAcceptance)
	case strings.TrimSpace(acceptance.IdempotencyKey) == "":
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidGoalAcceptance)
	default:
		return nil
	}
}
