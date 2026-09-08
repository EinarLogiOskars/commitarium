package workflow

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

const FeatureStateChangedPayloadVersion = 1

type FeatureStateChangedPayload struct {
	PreviousState feature.State `json:"previous_state"`
	State         feature.State `json:"state"`
}

var ErrInvalidPayload = errors.New("invalid workflow event payload")
var ErrUnsupportedPayloadVersion = errors.New("unsupported workflow event payload version")

func EncodeFeatureStateChangedPayload(
	previousState feature.State,
	state feature.State,
) (string, error) {
	if err := feature.ValidateTransition(previousState, state); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}

	payload, err := json.Marshal(FeatureStateChangedPayload{
		PreviousState: previousState,
		State:         state,
	})
	if err != nil {
		return "", fmt.Errorf("encode feature state change: %w", err)
	}

	return string(payload), nil
}

func DecodeFeatureStateChangedPayload(
	payloadVersion int,
	payload string,
) (FeatureStateChangedPayload, error) {
	if payloadVersion != FeatureStateChangedPayloadVersion {
		return FeatureStateChangedPayload{}, fmt.Errorf(
			"%w: feature state change version %d",
			ErrUnsupportedPayloadVersion,
			payloadVersion,
		)
	}

	decoded := FeatureStateChangedPayload{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return FeatureStateChangedPayload{}, fmt.Errorf(
			"%w: decode feature state change: %v",
			ErrInvalidPayload,
			err,
		)
	}

	if err := feature.ValidateTransition(
		decoded.PreviousState,
		decoded.State,
	); err != nil {
		return FeatureStateChangedPayload{}, fmt.Errorf(
			"%w: %w",
			ErrInvalidPayload,
			err,
		)
	}

	return decoded, nil
}
