package workflow

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

func TestReplayFeatureState(t *testing.T) {
	events := []Event{
		stateChangedTestEvent(t, 1, feature.StateDraft, feature.StatePlanning),
		stateChangedTestEvent(t, 2, feature.StatePlanning, feature.StateImplementing),
		stateChangedTestEvent(t, 3, feature.StateImplementing, feature.StateReviewing),
		stateChangedTestEvent(t, 4, feature.StateReviewing, feature.StateReadyToMerge),
	}

	state, err := ReplayFeatureState(events)
	if err != nil {
		t.Fatalf("replay feature state: %v", err)
	}
	if state != feature.StateReadyToMerge {
		t.Errorf("expected state %q, got %q", feature.StateReadyToMerge, state)
	}
}

func TestReplayFeatureStateWithoutEventsReturnsDraft(t *testing.T) {
	state, err := ReplayFeatureState(nil)
	if err != nil {
		t.Fatalf("replay empty event stream: %v", err)
	}
	if state != feature.StateDraft {
		t.Errorf("expected state %q, got %q", feature.StateDraft, state)
	}
}

func TestReplayFeatureStateRejectsInvalidStream(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []Event)
	}{
		{
			name: "sequence gap",
			mutate: func(_ *testing.T, events []Event) {
				events[1].Sequence = 3
			},
		},
		{
			name: "different aggregate",
			mutate: func(_ *testing.T, events []Event) {
				events[1].AggregateID = "fea_other"
			},
		},
		{
			name: "mismatched previous state",
			mutate: func(t *testing.T, events []Event) {
				payload, err := EncodeFeatureStateChangedPayload(
					feature.StateDraft,
					feature.StatePlanning,
				)
				if err != nil {
					t.Fatalf("encode replacement payload: %v", err)
				}
				events[1].Payload = payload
			},
		},
		{
			name: "unsupported event type",
			mutate: func(_ *testing.T, events []Event) {
				events[1].Type = EventType("unknown")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []Event{
				stateChangedTestEvent(t, 1, feature.StateDraft, feature.StatePlanning),
				stateChangedTestEvent(t, 2, feature.StatePlanning, feature.StateImplementing),
			}
			test.mutate(t, events)

			_, err := ReplayFeatureState(events)
			if !errors.Is(err, ErrInvalidEventStream) {
				t.Fatalf("expected error %v, got %v", ErrInvalidEventStream, err)
			}
		})
	}
}

func stateChangedTestEvent(
	t *testing.T,
	sequence int64,
	previousState feature.State,
	state feature.State,
) Event {
	t.Helper()

	event := validTestEvent(t)
	event.ID = fmt.Sprintf("evt_%d", sequence)
	event.Sequence = sequence
	payload, err := EncodeFeatureStateChangedPayload(previousState, state)
	if err != nil {
		t.Fatalf("encode state-change payload: %v", err)
	}
	event.Payload = payload
	return event
}
