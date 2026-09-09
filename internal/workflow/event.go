package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EventType string

const (
	EventTypeFeatureStateChanged EventType = "feature.state_changed"
	EventTypeGoalAccepted        EventType = "feature.goal_accepted"
)

type ActorKind string

const (
	ActorKindUser        ActorKind = "user"
	ActorKindAgent       ActorKind = "agent"
	ActorKindCoordinator ActorKind = "coordinator"
)

type Actor struct {
	Kind ActorKind
	ID   string
}

// Event is an append-only workflow fact. Persisted events must not be updated.
type Event struct {
	ID             string
	AggregateID    string
	Type           EventType
	Actor          Actor
	OccurredAt     time.Time
	Sequence       int64
	PayloadVersion int
	IdempotencyKey string
	Payload        string
}

var ErrInvalidEvent = errors.New("invalid workflow event")

func (kind ActorKind) IsValid() bool {
	switch kind {
	case ActorKindUser, ActorKindAgent, ActorKindCoordinator:
		return true
	default:
		return false
	}
}

func (event Event) Validate() error {
	switch {
	case strings.TrimSpace(event.ID) == "":
		return fmt.Errorf("%w: ID is required", ErrInvalidEvent)
	case strings.TrimSpace(event.AggregateID) == "":
		return fmt.Errorf("%w: aggregate ID is required", ErrInvalidEvent)
	case strings.TrimSpace(string(event.Type)) == "":
		return fmt.Errorf("%w: type is required", ErrInvalidEvent)
	case !event.Actor.Kind.IsValid():
		return fmt.Errorf(
			"%w: actor kind %q is not recognized",
			ErrInvalidEvent,
			event.Actor.Kind,
		)
	case strings.TrimSpace(event.Actor.ID) == "":
		return fmt.Errorf("%w: actor ID is required", ErrInvalidEvent)
	case event.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidEvent)
	case event.Sequence < 1:
		return fmt.Errorf("%w: sequence must be positive", ErrInvalidEvent)
	case event.PayloadVersion < 1:
		return fmt.Errorf("%w: payload version must be positive", ErrInvalidEvent)
	case strings.TrimSpace(event.IdempotencyKey) == "":
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidEvent)
	case !json.Valid([]byte(event.Payload)):
		return fmt.Errorf("%w: payload must contain valid JSON", ErrInvalidEvent)
	default:
		return nil
	}
}
