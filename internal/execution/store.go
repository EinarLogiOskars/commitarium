package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type PendingEvent struct {
	ID         string
	SessionID  string
	Type       worker.EventType
	Text       string
	OccurredAt time.Time
}

type RunTransition struct {
	RunID      string
	Expected   RunStatus
	Status     RunStatus
	Reason     string
	OccurredAt time.Time
}

type SessionTransition struct {
	SessionID         string
	Expected          SessionStatus
	Status            SessionStatus
	ProviderSessionID string
	OccurredAt        time.Time
}

type CommandResolution struct {
	CommandID string
	Status    CommandStatus
	AppliedAt time.Time
	Error     string
}

type Store interface {
	CreateRun(ctx context.Context, run Run) error
	GetRun(ctx context.Context, id string) (Run, error)
	TransitionRun(ctx context.Context, transition RunTransition) (Run, error)
	CreateSession(ctx context.Context, session Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	TransitionSession(ctx context.Context, transition SessionTransition) (Session, error)
	AppendEvent(ctx context.Context, event PendingEvent) (Event, error)
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
	CreateCommand(ctx context.Context, command Command) (Command, bool, error)
	GetCommand(ctx context.Context, id string) (Command, error)
	ResolveCommand(ctx context.Context, resolution CommandResolution) (Command, error)
}

var ErrAlreadyExists = errors.New("execution record already exists")
var ErrNotFound = errors.New("execution record not found")
var ErrEventConflict = errors.New("execution event ID reused for different content")
var ErrCommandConflict = errors.New("execution command ID reused for different content")
var ErrStateConflict = errors.New("execution record is not in the expected state")
var ErrRecordConflict = errors.New("execution record ID reused for different content")

func (event PendingEvent) Validate() error {
	switch {
	case strings.TrimSpace(event.ID) == "":
		return fmt.Errorf("%w: ID is required", ErrInvalidEvent)
	case strings.TrimSpace(event.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidEvent)
	case strings.TrimSpace(string(event.Type)) == "":
		return fmt.Errorf("%w: type is required", ErrInvalidEvent)
	case strings.TrimSpace(event.Text) == "":
		return fmt.Errorf("%w: text is required", ErrInvalidEvent)
	case event.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidEvent)
	default:
		return nil
	}
}

func (transition RunTransition) Validate() error {
	switch {
	case strings.TrimSpace(transition.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidStatusTransition)
	case !transition.Expected.CanTransitionTo(transition.Status):
		return fmt.Errorf(
			"%w: run %q to %q",
			ErrInvalidStatusTransition,
			transition.Expected,
			transition.Status,
		)
	case transition.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidStatusTransition)
	default:
		return nil
	}
}

func (transition SessionTransition) Validate() error {
	switch {
	case strings.TrimSpace(transition.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidStatusTransition)
	case !transition.Expected.CanTransitionTo(transition.Status):
		return fmt.Errorf(
			"%w: session %q to %q",
			ErrInvalidStatusTransition,
			transition.Expected,
			transition.Status,
		)
	case transition.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidStatusTransition)
	default:
		return nil
	}
}

func (resolution CommandResolution) Validate() error {
	switch {
	case strings.TrimSpace(resolution.CommandID) == "":
		return fmt.Errorf("%w: command ID is required", ErrInvalidCommand)
	case resolution.Status != CommandStatusApplied &&
		resolution.Status != CommandStatusRejected:
		return fmt.Errorf("%w: resolution status must be applied or rejected", ErrInvalidCommand)
	case resolution.AppliedAt.IsZero():
		return fmt.Errorf("%w: applied time is required", ErrInvalidCommand)
	case resolution.Status == CommandStatusApplied && resolution.Error != "":
		return fmt.Errorf("%w: applied command cannot have an error", ErrInvalidCommand)
	case resolution.Status == CommandStatusRejected && strings.TrimSpace(resolution.Error) == "":
		return fmt.Errorf("%w: rejected command requires an error", ErrInvalidCommand)
	default:
		return nil
	}
}
