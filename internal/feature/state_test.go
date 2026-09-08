package feature

import (
	"errors"
	"testing"
)

func TestValidateTransitionAllowsWorkflowEdges(t *testing.T) {
	tests := []struct {
		name    string
		current State
		next    State
	}{
		{name: "start discovery", current: StateDraft, next: StateDiscovery},
		{name: "begin planning", current: StateDiscovery, next: StatePlanning},
		{
			name:    "escalate planning disagreement",
			current: StatePlanning,
			next:    StateAwaitingUserDecision,
		},
		{
			name:    "resume planning after user decision",
			current: StateAwaitingUserDecision,
			next:    StatePlanning,
		},
		{
			name:    "accept plan",
			current: StatePlanning,
			next:    StateReadyToImplement,
		},
		{
			name:    "begin implementation",
			current: StateReadyToImplement,
			next:    StateImplementing,
		},
		{
			name:    "return implementation to planning",
			current: StateImplementing,
			next:    StatePlanning,
		},
		{
			name:    "open internal pull request",
			current: StateImplementing,
			next:    StateInternalPROpen,
		},
		{
			name:    "begin initial review",
			current: StateInternalPROpen,
			next:    StateReviewing,
		},
		{
			name:    "request changes",
			current: StateReviewing,
			next:    StateChangesRequested,
		},
		{
			name:    "address requested changes",
			current: StateChangesRequested,
			next:    StateImplementing,
		},
		{
			name:    "resume review on existing pull request",
			current: StateImplementing,
			next:    StateReviewing,
		},
		{
			name:    "approve reviewed feature",
			current: StateReviewing,
			next:    StateApproved,
		},
		{
			name:    "satisfy merge gates",
			current: StateApproved,
			next:    StateReadyToMerge,
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

func TestValidateTransitionAllowsExceptionalTerminalStates(t *testing.T) {
	nonTerminalStates := []State{
		StateDraft,
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
	}

	for _, current := range nonTerminalStates {
		for _, terminal := range []State{StateCancelled, StateFailed} {
			t.Run(string(current)+"_to_"+string(terminal), func(t *testing.T) {
				if err := ValidateTransition(current, terminal); err != nil {
					t.Fatalf(
						"expected transition from %q to %q to be valid: %v",
						current,
						terminal,
						err,
					)
				}
			})
		}
	}
}

func TestValidateTransitionRejectsInvalidEdges(t *testing.T) {
	tests := []struct {
		name    string
		current State
		next    State
	}{
		{name: "skip discovery", current: StateDraft, next: StatePlanning},
		{name: "skip planning", current: StateDiscovery, next: StateImplementing},
		{name: "skip implementation", current: StatePlanning, next: StateReviewing},
		{name: "approve without review", current: StateImplementing, next: StateApproved},
		{name: "merge before approval", current: StateReviewing, next: StateCompleted},
		{name: "repeat state", current: StatePlanning, next: StatePlanning},
		{name: "unknown current state", current: State("unknown"), next: StateDiscovery},
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
		StateFailed,
	}
	allStates := []State{
		StateDraft,
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
		StateFailed,
	}

	for _, current := range terminalStates {
		for _, next := range allStates {
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
	tests := []struct {
		state    State
		valid    bool
		terminal bool
	}{
		{state: StateDraft, valid: true},
		{state: StateReadyToMerge, valid: true},
		{state: StateCompleted, valid: true, terminal: true},
		{state: StateCancelled, valid: true, terminal: true},
		{state: StateFailed, valid: true, terminal: true},
		{state: State("unknown")},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			if actual := test.state.IsValid(); actual != test.valid {
				t.Errorf("expected IsValid to be %t, got %t", test.valid, actual)
			}
			if actual := test.state.IsTerminal(); actual != test.terminal {
				t.Errorf("expected IsTerminal to be %t, got %t", test.terminal, actual)
			}
		})
	}
}
