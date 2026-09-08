package feature

import (
	"errors"
	"testing"
)

var workflowStates = []State{
	StateDraft,
	StatePlanning,
	StateImplementing,
	StateReviewing,
	StateReadyToMerge,
	StateCompleted,
	StateCancelled,
}

func TestValidateTransitionAllowsWorkflowEdges(t *testing.T) {
	tests := []struct {
		name    string
		current State
		next    State
	}{
		{name: "start planning", current: StateDraft, next: StatePlanning},
		{
			name:    "return planning to goal drafting",
			current: StatePlanning,
			next:    StateDraft,
		},
		{
			name:    "begin implementation",
			current: StatePlanning,
			next:    StateImplementing,
		},
		{
			name:    "return implementation to planning",
			current: StateImplementing,
			next:    StatePlanning,
		},
		{
			name:    "begin review",
			current: StateImplementing,
			next:    StateReviewing,
		},
		{
			name:    "return review to planning",
			current: StateReviewing,
			next:    StatePlanning,
		},
		{
			name:    "satisfy review and merge gates",
			current: StateReviewing,
			next:    StateReadyToMerge,
		},
		{
			name:    "invalidate merge readiness",
			current: StateReadyToMerge,
			next:    StateReviewing,
		},
		{
			name:    "confirm Forgejo merge",
			current: StateReadyToMerge,
			next:    StateCompleted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateTransition(test.current, test.next); err != nil {
				t.Fatalf(
					"expected transition from %q to %q to be valid: %v",
					test.current,
					test.next,
					err,
				)
			}
		})
	}
}

func TestValidateTransitionAllowsCancellationFromEveryActivePhase(t *testing.T) {
	activeStates := []State{
		StateDraft,
		StatePlanning,
		StateImplementing,
		StateReviewing,
		StateReadyToMerge,
	}

	for _, current := range activeStates {
		t.Run(string(current), func(t *testing.T) {
			if err := ValidateTransition(current, StateCancelled); err != nil {
				t.Fatalf(
					"expected transition from %q to %q to be valid: %v",
					current,
					StateCancelled,
					err,
				)
			}
		})
	}
}

func TestValidateTransitionRejectsInvalidEdges(t *testing.T) {
	tests := []struct {
		name    string
		current State
		next    State
	}{
		{name: "skip planning", current: StateDraft, next: StateImplementing},
		{name: "skip implementation", current: StatePlanning, next: StateReviewing},
		{name: "skip review", current: StateImplementing, next: StateReadyToMerge},
		{name: "merge before ready", current: StateReviewing, next: StateCompleted},
		{name: "repeat state", current: StatePlanning, next: StatePlanning},
		{name: "unknown current state", current: State("unknown"), next: StatePlanning},
		{name: "unknown next state", current: StateDraft, next: State("unknown")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateTransition(test.current, test.next)

			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("expected error %v, got %v", ErrInvalidTransition, err)
			}
		})
	}
}

func TestTerminalStatesRejectAllTransitions(t *testing.T) {
	terminalStates := []State{
		StateCompleted,
		StateCancelled,
	}

	for _, current := range terminalStates {
		for _, next := range workflowStates {
			t.Run(string(current)+"_to_"+string(next), func(t *testing.T) {
				err := ValidateTransition(current, next)

				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("expected error %v, got %v", ErrInvalidTransition, err)
				}
			})
		}
	}
}

func TestStateClassification(t *testing.T) {
	for _, state := range workflowStates {
		t.Run(string(state), func(t *testing.T) {
			if !state.IsValid() {
				t.Errorf("expected state %q to be valid", state)
			}

			expectedTerminal := state == StateCompleted ||
				state == StateCancelled
			if actual := state.IsTerminal(); actual != expectedTerminal {
				t.Errorf(
					"expected IsTerminal to be %t, got %t",
					expectedTerminal,
					actual,
				)
			}
		})
	}

	unknown := State("unknown")
	if unknown.IsValid() {
		t.Errorf("expected state %q to be invalid", unknown)
	}
	if unknown.IsTerminal() {
		t.Errorf("expected state %q not to be terminal", unknown)
	}
}
