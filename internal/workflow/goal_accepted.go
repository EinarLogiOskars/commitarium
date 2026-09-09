package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const GoalAcceptedPayloadVersion = 1

type GoalAcceptedPayload struct {
	Goal      string `json:"goal"`
	SessionID string `json:"session_id"`
}

func EncodeGoalAcceptedPayload(goal, sessionID string) (string, error) {
	payload := GoalAcceptedPayload{
		Goal: strings.TrimSpace(goal), SessionID: strings.TrimSpace(sessionID),
	}
	if payload.Goal == "" || payload.SessionID == "" {
		return "", fmt.Errorf("%w: goal and session ID are required", ErrInvalidPayload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode accepted goal: %w", err)
	}
	return string(encoded), nil
}

func DecodeGoalAcceptedPayload(
	payloadVersion int,
	payload string,
) (GoalAcceptedPayload, error) {
	if payloadVersion != GoalAcceptedPayloadVersion {
		return GoalAcceptedPayload{}, fmt.Errorf(
			"%w: goal acceptance version %d", ErrUnsupportedPayloadVersion, payloadVersion,
		)
	}
	decoded := GoalAcceptedPayload{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return GoalAcceptedPayload{}, fmt.Errorf("%w: decode accepted goal: %v", ErrInvalidPayload, err)
	}
	if strings.TrimSpace(decoded.Goal) == "" || decoded.Goal != strings.TrimSpace(decoded.Goal) ||
		strings.TrimSpace(decoded.SessionID) == "" || decoded.SessionID != strings.TrimSpace(decoded.SessionID) {
		return GoalAcceptedPayload{}, fmt.Errorf(
			"%w: accepted goal and session ID must be non-empty and trimmed", ErrInvalidPayload,
		)
	}
	return decoded, nil
}
