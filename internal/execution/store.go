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

type PendingWorkerEvent struct {
	ID             string
	SessionID      string
	AttemptID      string
	SourceSequence int64
	Type           worker.EventType
	Text           string
	OccurredAt     time.Time
	AcceptedAt     time.Time
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
	Result            *worker.Result
	OccurredAt        time.Time
}

// SessionRecovery atomically marks a nonterminal session as waiting at its
// recovery boundary and allocates a durable attempt number.
type SessionRecovery struct {
	SessionID  string
	Expected   SessionStatus
	OccurredAt time.Time
}

type CommandResolution struct {
	CommandID string
	Status    CommandStatus
	AppliedAt time.Time
	Error     string
}

// WorkerTurnAdmission is the complete durable boundary for another provider
// turn in an existing session. The command, user-visible message, replacement
// event cursor, and running states must either all commit or all roll back.
type WorkerTurnAdmission struct {
	Command                   Command
	UserEvent                 PendingEvent
	PreviousAttemptID         string
	PreviousLastEventSequence int64
	NextAttempt               WorkerAttemptCheckpoint
	RunReason                 string
	OccurredAt                time.Time
}

type WorkerTurnAdmissionResult struct {
	Command   Command
	UserEvent Event
}

// AutonomousTurnAdmission is the durable boundary for a coordinator-started
// provider turn. Unlike WorkerTurnAdmission, it has no user command or user
// message to record; it only replaces the worker attempt and marks the
// existing session and run as active.
type AutonomousTurnAdmission struct {
	SessionID                 string
	PreviousAttemptID         string
	PreviousLastEventSequence int64
	NextAttempt               WorkerAttemptCheckpoint
	RunReason                 string
	OccurredAt                time.Time
	RunAlreadyActive          bool
}

// NewSessionTurnAdmission creates a new durable logical conversation and its
// first provider attempt while returning the existing run to active work.
type NewSessionTurnAdmission struct {
	Session    Session
	Attempt    WorkerAttemptCheckpoint
	RunReason  string
	OccurredAt time.Time
}

type PendingPlanningMessage struct {
	RunID    string
	EventID  string
	LinkedAt time.Time
}

type Store interface {
	CreateRun(ctx context.Context, run Run) error
	GetRun(ctx context.Context, id string) (Run, error)
	TransitionRun(ctx context.Context, transition RunTransition) (Run, error)
	CreateSession(ctx context.Context, session Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessions(ctx context.Context, runID string) ([]Session, error)
	ListRecoverableRuns(ctx context.Context) ([]Run, error)
	ListActiveSessions(ctx context.Context, runID string) ([]Session, error)
	TransitionSession(ctx context.Context, transition SessionTransition) (Session, error)
	BeginSessionRecovery(ctx context.Context, recovery SessionRecovery) (Session, error)
	AppendEvent(ctx context.Context, event PendingEvent) (Event, bool, error)
	CreateWorkerAttempt(ctx context.Context, checkpoint WorkerAttemptCheckpoint) (WorkerAttemptCheckpoint, bool, error)
	GetWorkerAttempt(ctx context.Context, sessionID string) (WorkerAttemptCheckpoint, error)
	AppendWorkerEvent(ctx context.Context, event PendingWorkerEvent) (Event, bool, error)
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
	CreateCommand(ctx context.Context, command Command) (Command, bool, error)
	GetCommand(ctx context.Context, id string) (Command, error)
	ListPendingCommands(ctx context.Context, sessionID string) ([]Command, error)
	ResolveCommand(ctx context.Context, resolution CommandResolution) (Command, error)
	BeginWorkerTurn(ctx context.Context, admission WorkerTurnAdmission) (WorkerTurnAdmissionResult, bool, error)
	BeginAutonomousTurn(ctx context.Context, admission AutonomousTurnAdmission) (bool, error)
	BeginNewSessionTurn(ctx context.Context, admission NewSessionTurnAdmission) (bool, error)
	LinkPlanningMessage(ctx context.Context, message PendingPlanningMessage) (PlanningMessage, bool, error)
	ListPlanningMessages(ctx context.Context, runID string) ([]PlanningMessage, error)
}

var ErrAlreadyExists = errors.New("execution record already exists")
var ErrNotFound = errors.New("execution record not found")
var ErrEventConflict = errors.New("execution event ID reused for different content")
var ErrWorkerAttemptConflict = errors.New("session is bound to a different worker attempt")
var ErrWorkerEventConflict = errors.New("worker event sequence was reused for different content")
var ErrWorkerEventSequence = errors.New("worker event sequence is not the next expected value")
var ErrCommandConflict = errors.New("execution command ID reused for different content")
var ErrStateConflict = errors.New("execution record is not in the expected state")
var ErrRecordConflict = errors.New("execution record ID reused for different content")
var ErrPlanningMessageConflict = errors.New("session event is already linked to a different planning conversation")

func (message PendingPlanningMessage) Validate() error {
	switch {
	case strings.TrimSpace(message.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidPlanningMessage)
	case strings.TrimSpace(message.EventID) == "":
		return fmt.Errorf("%w: event ID is required", ErrInvalidPlanningMessage)
	case message.LinkedAt.IsZero():
		return fmt.Errorf("%w: link time is required", ErrInvalidPlanningMessage)
	default:
		return nil
	}
}

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

func (event PendingWorkerEvent) Validate() error {
	switch {
	case strings.TrimSpace(event.ID) == "":
		return fmt.Errorf("%w: ID is required", ErrInvalidEvent)
	case strings.TrimSpace(event.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidEvent)
	case strings.TrimSpace(event.AttemptID) == "":
		return fmt.Errorf("%w: worker attempt ID is required", ErrInvalidEvent)
	case event.SourceSequence < 1:
		return fmt.Errorf("%w: worker event sequence must be positive", ErrInvalidEvent)
	case strings.TrimSpace(string(event.Type)) == "":
		return fmt.Errorf("%w: type is required", ErrInvalidEvent)
	case strings.TrimSpace(event.Text) == "":
		return fmt.Errorf("%w: text is required", ErrInvalidEvent)
	case event.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidEvent)
	case event.AcceptedAt.IsZero():
		return fmt.Errorf("%w: acceptance time is required", ErrInvalidEvent)
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

func (recovery SessionRecovery) Validate() error {
	switch {
	case strings.TrimSpace(recovery.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidStatusTransition)
	case recovery.Expected != SessionStatusRunning &&
		recovery.Expected != SessionStatusPauseRequested &&
		recovery.Expected != SessionStatusPaused:
		return fmt.Errorf("%w: session %q cannot begin recovery", ErrInvalidStatusTransition, recovery.Expected)
	case recovery.OccurredAt.IsZero():
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
