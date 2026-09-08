package feature

import (
	"errors"
	"fmt"
)

type State string

const (
	StateDraft                State = "draft"
	StateDiscovery            State = "discovery"
	StatePlanning             State = "planning"
	StateAwaitingUserDecision State = "awaiting_user_decision"
	StateReadyToImplement     State = "ready_to_implement"
	StateImplementing         State = "implementing"
	StateInternalPROpen       State = "internal_pr_open"
	StateReviewing            State = "reviewing"
	StateChangesRequested     State = "changes_requested"
	StateApproved             State = "approved"
	StateReadyToMerge         State = "ready_to_merge"
	StateCompleted            State = "completed"
	StateCancelled            State = "cancelled"
	StateFailed               State = "failed"
)

var ErrInvalidTransition = errors.New("invalid feature state transition")

func (state State) IsValid() bool {
	switch state {
	case StateDraft,
		StateDiscovery,
		StatePlanning,
		StateAwaitingUserDecision,
		StateReadyToImplement,
		StateImplementing,
		StateInternalPROpen,
		StateReviewing,
		StateChangesRequested,
		StateApproved,
		StateReadyToMerge,
		StateCompleted,
		StateCancelled,
		StateFailed:
		return true
	default:
		return false
	}
}

func (state State) IsTerminal() bool {
	return state == StateCompleted ||
		state == StateCancelled ||
		state == StateFailed
}

func (state State) CanTransitionTo(next State) bool {
	if !state.IsValid() || !next.IsValid() || state.IsTerminal() {
		return false
	}

	if next == StateCancelled || next == StateFailed {
		return true
	}

	switch state {
	case StateDraft:
		return next == StateDiscovery
	case StateDiscovery:
		return next == StatePlanning
	case StatePlanning:
		return next == StateAwaitingUserDecision ||
			next == StateReadyToImplement
	case StateAwaitingUserDecision:
		return next == StatePlanning
	case StateReadyToImplement:
		return next == StateImplementing
	case StateImplementing:
		return next == StatePlanning ||
			next == StateInternalPROpen ||
			next == StateReviewing
	case StateInternalPROpen:
		return next == StateReviewing
	case StateReviewing:
		return next == StateChangesRequested ||
			next == StateApproved
	case StateChangesRequested:
		return next == StateImplementing
	case StateApproved:
		return next == StateReadyToMerge
	case StateReadyToMerge:
		return next == StateCompleted
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
