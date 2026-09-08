package feature

import (
	"errors"
	"fmt"
)

type State string

const (
	StateDraft        State = "draft"
	StatePlanning     State = "planning"
	StateImplementing State = "implementing"
	StateReviewing    State = "reviewing"
	StateReadyToMerge State = "ready_to_merge"
	StateCompleted    State = "completed"
	StateCancelled    State = "cancelled"
)

var ErrInvalidTransition = errors.New("invalid feature state transition")

func (state State) IsValid() bool {
	switch state {
	case StateDraft,
		StatePlanning,
		StateImplementing,
		StateReviewing,
		StateReadyToMerge,
		StateCompleted,
		StateCancelled:
		return true
	default:
		return false
	}
}

func (state State) IsTerminal() bool {
	return state == StateCompleted ||
		state == StateCancelled
}

func (state State) CanTransitionTo(next State) bool {
	if !state.IsValid() || !next.IsValid() || state.IsTerminal() {
		return false
	}

	if next == StateCancelled {
		return true
	}

	switch state {
	case StateDraft:
		return next == StatePlanning
	case StatePlanning:
		return next == StateDraft ||
			next == StateImplementing
	case StateImplementing:
		return next == StatePlanning ||
			next == StateReviewing
	case StateReviewing:
		return next == StatePlanning ||
			next == StateReadyToMerge
	case StateReadyToMerge:
		return next == StateReviewing ||
			next == StateCompleted
	default:
		return false
	}
}

func ValidateTransition(current State, next State) error {
	if current.CanTransitionTo(next) {
		return nil
	}

	return fmt.Errorf(
		"%w: %q to %q",
		ErrInvalidTransition,
		current,
		next,
	)
}
