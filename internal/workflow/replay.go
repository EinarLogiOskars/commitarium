package workflow

import (
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

var ErrInvalidEventStream = errors.New("invalid workflow event stream")

func ReplayFeatureState(events []Event) (feature.State, error) {
	state := feature.StateDraft
	var aggregateID string

	for index, event := range events {
		expectedSequence := int64(index + 1)
		if err := event.Validate(); err != nil {
			return "", fmt.Errorf(
				"%w: event at sequence %d: %v",
				ErrInvalidEventStream,
				expectedSequence,
				err,
			)
		}
		if event.Sequence != expectedSequence {
			return "", fmt.Errorf(
				"%w: expected sequence %d, got %d",
				ErrInvalidEventStream,
				expectedSequence,
				event.Sequence,
			)
		}
		if index == 0 {
			aggregateID = event.AggregateID
		} else if event.AggregateID != aggregateID {
			return "", fmt.Errorf(
				"%w: expected aggregate %q, got %q",
				ErrInvalidEventStream,
				aggregateID,
				event.AggregateID,
			)
		}
		if event.Type != EventTypeFeatureStateChanged {
			return "", fmt.Errorf(
				"%w: unsupported event type %q",
				ErrInvalidEventStream,
				event.Type,
			)
		}

		payload, err := DecodeFeatureStateChangedPayload(
			event.PayloadVersion,
			event.Payload,
		)
		if err != nil {
			return "", fmt.Errorf(
				"%w: event %q: %v",
				ErrInvalidEventStream,
				event.ID,
				err,
			)
		}
		if payload.PreviousState != state {
			return "", fmt.Errorf(
				"%w: event %q expected previous state %q, got %q",
				ErrInvalidEventStream,
				event.ID,
				state,
				payload.PreviousState,
			)
		}
		state = payload.State
	}

	return state, nil
}
