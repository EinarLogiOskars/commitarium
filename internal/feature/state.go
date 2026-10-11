package feature

import (
	"errors"
	"fmt"
)

type State string

const (
	StateDraft State = "draft"
	// StateReady is a clarified work order with a handoff brief, waiting for
	// the user to start it.
	StateReady        State = "ready"
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
		StateReady,
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
		return next == StateReady ||
			next == StatePlanning
	case StateReady:
		return next == StateDraft ||
			next == StatePlanning
	// A started work order can return to Ready to start over from its brief
	// (ADR-017).
	case StatePlanning:
		return next == StateDraft ||
			next == StateReady ||
			next == StateImplementing
	// Without a reviewer (ADR-018), implementation goes straight to the
	// merge gate.
	case StateImplementing:
		return next == StatePlanning ||
			next == StateReady ||
			next == StateReviewing ||
			next == StateReadyToMerge
	case StateReviewing:
		return next == StatePlanning ||
			next == StateReady ||
			next == StateReadyToMerge
	case StateReadyToMerge:
		return next == StatePlanning ||
			next == StateReady ||
			next == StateReviewing ||
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
