package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

func TestEventValidate(t *testing.T) {
	event := validTestEvent(t)

	if err := event.Validate(); err != nil {
		t.Fatalf("validate event: %v", err)
	}
}

func TestEventValidateRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing ID", mutate: func(event *Event) { event.ID = "" }},
		{
			name:   "missing aggregate ID",
			mutate: func(event *Event) { event.AggregateID = "" },
		},
		{name: "missing type", mutate: func(event *Event) { event.Type = "" }},
		{
			name: "unknown actor kind",
			mutate: func(event *Event) {
				event.Actor.Kind = ActorKind("unknown")
			},
		},
		{
			name:   "missing actor ID",
			mutate: func(event *Event) { event.Actor.ID = "" },
		},
		{
			name:   "missing occurrence time",
			mutate: func(event *Event) { event.OccurredAt = time.Time{} },
		},
		{name: "zero sequence", mutate: func(event *Event) { event.Sequence = 0 }},
		{
			name:   "zero payload version",
			mutate: func(event *Event) { event.PayloadVersion = 0 },
		},
		{
			name:   "missing idempotency key",
			mutate: func(event *Event) { event.IdempotencyKey = "" },
		},
		{
			name:   "invalid JSON payload",
			mutate: func(event *Event) { event.Payload = "{" },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validTestEvent(t)
			test.mutate(&event)

			err := event.Validate()
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("expected error %v, got %v", ErrInvalidEvent, err)
			}
		})
	}
}

func TestActorKindIsValid(t *testing.T) {
	for _, kind := range []ActorKind{
		ActorKindUser,
		ActorKindAgent,
		ActorKindCoordinator,
	} {
		if !kind.IsValid() {
			t.Errorf("expected actor kind %q to be valid", kind)
		}
	}

	if ActorKind("unknown").IsValid() {
		t.Fatal("expected unknown actor kind to be invalid")
	}
}

func TestFeatureStateChangedPayloadRoundTrip(t *testing.T) {
	encoded, err := EncodeFeatureStateChangedPayload(
		feature.StatePlanning,
		feature.StateImplementing,
	)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	decoded, err := DecodeFeatureStateChangedPayload(
		FeatureStateChangedPayloadVersion,
		encoded,
	)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	expected := FeatureStateChangedPayload{
		PreviousState: feature.StatePlanning,
		State:         feature.StateImplementing,
	}
	if decoded != expected {
		t.Errorf("expected payload %+v, got %+v", expected, decoded)
	}
}

func TestFeatureStateChangedPayloadRejectsInvalidTransition(t *testing.T) {
	_, err := EncodeFeatureStateChangedPayload(
		feature.StateDraft,
		feature.StateReviewing,
	)

	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("expected error %v, got %v", ErrInvalidPayload, err)
	}
	if !errors.Is(err, feature.ErrInvalidTransition) {
		t.Fatalf("expected error %v, got %v", feature.ErrInvalidTransition, err)
	}
}

func TestDecodeFeatureStateChangedPayloadRejectsUnsupportedVersion(t *testing.T) {
	_, err := DecodeFeatureStateChangedPayload(2, `{}`)

	if !errors.Is(err, ErrUnsupportedPayloadVersion) {
		t.Fatalf(
			"expected error %v, got %v",
			ErrUnsupportedPayloadVersion,
			err,
		)
	}
}

func validTestEvent(t *testing.T) Event {
	t.Helper()

	payload, err := EncodeFeatureStateChangedPayload(
		feature.StateDraft,
		feature.StatePlanning,
	)
	if err != nil {
		t.Fatalf("encode test payload: %v", err)
	}

	return Event{
		ID:          "evt_test",
		AggregateID: "fea_test",
		Type:        EventTypeFeatureStateChanged,
		Actor: Actor{
			Kind: ActorKindUser,
			ID:   "usr_test",
		},
		OccurredAt:     time.Date(2026, time.September, 8, 15, 0, 0, 0, time.UTC),
		Sequence:       1,
		PayloadVersion: FeatureStateChangedPayloadVersion,
		IdempotencyKey: "cmd_test",
		Payload:        payload,
	}
}
