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

type Store interface {
	CreateRun(ctx context.Context, run Run) error
	GetRun(ctx context.Context, id string) (Run, error)
	CreateSession(ctx context.Context, session Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	AppendEvent(ctx context.Context, event PendingEvent) (Event, error)
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
	CreateCommand(ctx context.Context, command Command) (Command, error)
	GetCommand(ctx context.Context, id string) (Command, error)
}

var ErrAlreadyExists = errors.New("execution record already exists")
var ErrNotFound = errors.New("execution record not found")
var ErrEventConflict = errors.New("execution event ID reused for different content")
var ErrCommandConflict = errors.New("execution command ID reused for different content")

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
