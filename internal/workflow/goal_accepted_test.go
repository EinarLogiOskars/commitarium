package workflow

import (
	"errors"
	"testing"
)

func TestGoalAcceptedPayloadRoundTrip(t *testing.T) {
	payload, err := EncodeGoalAcceptedPayload(
		"Export reports as CSV.\n\n- Include a header row.",
		"ses_lead",
	)
	if err != nil {
		t.Fatalf("encode goal acceptance: %v", err)
	}

	decoded, err := DecodeGoalAcceptedPayload(GoalAcceptedPayloadVersion, payload)
	if err != nil {
		t.Fatalf("decode goal acceptance: %v", err)
	}
	if decoded.Goal != "Export reports as CSV.\n\n- Include a header row." ||
		decoded.SessionID != "ses_lead" {
		t.Fatalf("unexpected decoded payload %+v", decoded)
	}
}

func TestDecodeGoalAcceptedPayloadRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		version int
		payload string
		target  error
	}{
		{name: "unsupported version", version: 2, payload: `{}`, target: ErrUnsupportedPayloadVersion},
		{name: "blank goal", version: 1, payload: `{"goal":"","session_id":"ses_lead"}`, target: ErrInvalidPayload},
		{name: "untrimmed session", version: 1, payload: `{"goal":"Ship it","session_id":" ses_lead"}`, target: ErrInvalidPayload},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeGoalAcceptedPayload(test.version, test.payload)
			if !errors.Is(err, test.target) {
				t.Fatalf("expected error %v, got %v", test.target, err)
			}
		})
	}
}
