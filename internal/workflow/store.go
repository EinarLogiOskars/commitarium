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

type Store interface {
	ApplyFeatureTransition(
		ctx context.Context,
		transition FeatureTransition,
	) (Event, error)
	ListEvents(ctx context.Context, aggregateID string) ([]Event, error)
}

var ErrInvalidTransitionRequest = errors.New("invalid feature transition request")
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
