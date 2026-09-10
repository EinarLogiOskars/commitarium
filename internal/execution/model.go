package execution

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type RunStatus string

const (
	RunStatusRunning        RunStatus = "running"
	RunStatusWaitingForUser RunStatus = "waiting_for_user"
	RunStatusSucceeded      RunStatus = "succeeded"
	RunStatusStopped        RunStatus = "stopped"
	RunStatusFailed         RunStatus = "failed"
)

type Run struct {
	ID                             string
	FeatureID                      string
	Status                         RunStatus
	Reason                         string
	PlanningRoundLimit             int
	ImplementationReviewRoundLimit int
	StartedAt                      time.Time
	UpdatedAt                      time.Time
	EndedAt                        *time.Time
}

type SessionStatus string

const (
	SessionStatusStarting       SessionStatus = "starting"
	SessionStatusRunning        SessionStatus = "running"
	SessionStatusWaitingForUser SessionStatus = "waiting_for_user"
	SessionStatusPauseRequested SessionStatus = "pause_requested"
	SessionStatusPaused         SessionStatus = "paused"
	SessionStatusCompleted      SessionStatus = "completed"
	SessionStatusStopped        SessionStatus = "stopped"
	SessionStatusFailed         SessionStatus = "failed"
)

type Session struct {
	ID                string
	RunID             string
	AgentID           string
	Role              worker.Role
	Status            SessionStatus
	ProviderSessionID string
	Outcome           worker.Outcome
	Disposition       worker.Disposition
	Summary           string
	RecoveryAttempt   int
	StartedAt         time.Time
	UpdatedAt         time.Time
	EndedAt           *time.Time
}

type Event struct {
	ID                  string
	SessionID           string
	Sequence            int64
	Type                worker.EventType
	Text                string
	OccurredAt          time.Time
	WorkerAttemptID     string
	WorkerEventSequence int64
}

// PlanningMessage points at one existing session event in the shared,
// run-level planning discussion. The text remains stored only once, in the
// session event; this record adds the cross-session order seen by clients.
type PlanningMessage struct {
	RunID    string
	Sequence int64
	AgentID  string
	Role     worker.Role
	Event    Event
	LinkedAt time.Time
}

// WorkerAttemptCheckpoint identifies the one worker process incarnation whose
// events a coordinator session currently accepts. LastEventSequence is the
// durable reconnect cursor and advances only in the same transaction that
// stores the corresponding public session activity.
type WorkerAttemptCheckpoint struct {
	SessionID         string
	AttemptID         string
	LastEventSequence int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type CommandStatus string

const (
	CommandStatusPending  CommandStatus = "pending"
	CommandStatusApplied  CommandStatus = "applied"
	CommandStatusRejected CommandStatus = "rejected"
)

type Command struct {
	ID          string
	SessionID   string
	Type        worker.CommandType
	Message     string
	Status      CommandStatus
	RequestedAt time.Time
	AppliedAt   *time.Time
	Error       string
}

var ErrInvalidRun = errors.New("invalid execution run")
var ErrInvalidSession = errors.New("invalid execution session")
var ErrInvalidEvent = errors.New("invalid execution event")
var ErrInvalidPlanningMessage = errors.New("invalid planning message")
var ErrInvalidWorkerAttempt = errors.New("invalid worker attempt checkpoint")
var ErrInvalidCommand = errors.New("invalid execution command")
var ErrInvalidStatusTransition = errors.New("invalid execution status transition")

func (status RunStatus) IsValid() bool {
	switch status {
	case RunStatusRunning,
		RunStatusWaitingForUser,
		RunStatusSucceeded,
		RunStatusStopped,
		RunStatusFailed:
		return true
	default:
		return false
	}
}

func (status RunStatus) IsTerminal() bool {
	return status == RunStatusSucceeded ||
		status == RunStatusStopped ||
		status == RunStatusFailed
}

func (status RunStatus) CanTransitionTo(next RunStatus) bool {
	if !status.IsValid() || !next.IsValid() || status.IsTerminal() || status == next {
		return false
	}
	switch status {
	case RunStatusRunning:
		return next == RunStatusWaitingForUser || next.IsTerminal()
	case RunStatusWaitingForUser:
		return next == RunStatusRunning ||
			next == RunStatusStopped ||
			next == RunStatusFailed
	default:
		return false
	}
}

func (run Run) Validate() error {
	switch {
	case strings.TrimSpace(run.ID) == "":
		return fmt.Errorf("%w: ID is required", ErrInvalidRun)
	case strings.TrimSpace(run.FeatureID) == "":
		return fmt.Errorf("%w: feature ID is required", ErrInvalidRun)
	case !run.Status.IsValid():
		return fmt.Errorf("%w: status %q is not recognized", ErrInvalidRun, run.Status)
	case run.PlanningRoundLimit < 0:
		return fmt.Errorf("%w: planning round limit cannot be negative", ErrInvalidRun)
	case run.ImplementationReviewRoundLimit < 0:
		return fmt.Errorf("%w: implementation review round limit cannot be negative", ErrInvalidRun)
	case run.StartedAt.IsZero():
		return fmt.Errorf("%w: start time is required", ErrInvalidRun)
	case run.UpdatedAt.Before(run.StartedAt):
		return fmt.Errorf("%w: update time precedes start time", ErrInvalidRun)
	case run.Status.IsTerminal() && run.EndedAt == nil:
		return fmt.Errorf("%w: terminal run requires an end time", ErrInvalidRun)
	case !run.Status.IsTerminal() && run.EndedAt != nil:
		return fmt.Errorf("%w: active run cannot have an end time", ErrInvalidRun)
	case run.EndedAt != nil && run.EndedAt.Before(run.StartedAt):
		return fmt.Errorf("%w: end time precedes start time", ErrInvalidRun)
	case run.EndedAt != nil && run.UpdatedAt.Before(*run.EndedAt):
		return fmt.Errorf("%w: update time precedes end time", ErrInvalidRun)
	default:
		return nil
	}
}

func (status SessionStatus) IsValid() bool {
	switch status {
	case SessionStatusStarting,
		SessionStatusRunning,
		SessionStatusWaitingForUser,
		SessionStatusPauseRequested,
		SessionStatusPaused,
		SessionStatusCompleted,
		SessionStatusStopped,
		SessionStatusFailed:
		return true
	default:
		return false
	}
}

func (status SessionStatus) IsTerminal() bool {
	return status == SessionStatusCompleted ||
		status == SessionStatusStopped ||
		status == SessionStatusFailed
}

func (status SessionStatus) CanTransitionTo(next SessionStatus) bool {
	if !status.IsValid() || !next.IsValid() || status.IsTerminal() || status == next {
		return false
	}
	switch status {
	case SessionStatusStarting:
		return next == SessionStatusRunning ||
			next == SessionStatusStopped ||
			next == SessionStatusFailed
	case SessionStatusRunning:
		return next == SessionStatusWaitingForUser ||
			next == SessionStatusPauseRequested || next.IsTerminal()
	case SessionStatusWaitingForUser:
		return next == SessionStatusRunning ||
			next == SessionStatusStopped ||
			next == SessionStatusFailed
	case SessionStatusPauseRequested:
		return next == SessionStatusWaitingForUser ||
			next == SessionStatusPaused ||
			next == SessionStatusRunning ||
			next.IsTerminal()
	case SessionStatusPaused:
		return next == SessionStatusRunning ||
			next == SessionStatusStopped ||
			next == SessionStatusFailed
	default:
		return false
	}
}

func (session Session) Validate() error {
	switch {
	case strings.TrimSpace(session.ID) == "":
		return fmt.Errorf("%w: ID is required", ErrInvalidSession)
	case strings.TrimSpace(session.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidSession)
	case strings.TrimSpace(session.AgentID) == "":
		return fmt.Errorf("%w: agent ID is required", ErrInvalidSession)
	case !session.Role.IsValid():
		return fmt.Errorf("%w: role %q is not recognized", ErrInvalidSession, session.Role)
	case !session.Status.IsValid():
		return fmt.Errorf("%w: status %q is not recognized", ErrInvalidSession, session.Status)
	case session.StartedAt.IsZero():
		return fmt.Errorf("%w: start time is required", ErrInvalidSession)
	case session.UpdatedAt.Before(session.StartedAt):
		return fmt.Errorf("%w: update time precedes start time", ErrInvalidSession)
	case session.Status.IsTerminal() && session.EndedAt == nil:
		return fmt.Errorf("%w: terminal session requires an end time", ErrInvalidSession)
	case !session.Status.IsTerminal() && session.EndedAt != nil:
		return fmt.Errorf("%w: active session cannot have an end time", ErrInvalidSession)
	case session.EndedAt != nil && session.EndedAt.Before(session.StartedAt):
		return fmt.Errorf("%w: end time precedes start time", ErrInvalidSession)
	case session.EndedAt != nil && session.UpdatedAt.Before(*session.EndedAt):
		return fmt.Errorf("%w: update time precedes end time", ErrInvalidSession)
	case session.RecoveryAttempt < 0:
		return fmt.Errorf("%w: recovery attempt cannot be negative", ErrInvalidSession)
	case !session.Status.IsTerminal() &&
		(session.Outcome != "" || session.Disposition != "" || session.Summary != ""):
		return fmt.Errorf("%w: active session cannot have a worker result", ErrInvalidSession)
	case session.Status == SessionStatusCompleted && session.Outcome != "" &&
		(session.Outcome != worker.OutcomeCompleted || !session.Disposition.IsValid()):
		return fmt.Errorf("%w: completed session has an invalid worker result", ErrInvalidSession)
	case session.Status == SessionStatusStopped && session.Outcome != "" &&
		session.Outcome != worker.OutcomeStopped:
		return fmt.Errorf("%w: stopped session has an invalid worker result", ErrInvalidSession)
	case session.Status == SessionStatusFailed && session.Outcome != "" &&
		(session.Outcome != worker.OutcomeFailed || session.Disposition != ""):
		return fmt.Errorf("%w: failed session has an invalid worker result", ErrInvalidSession)
	default:
		return nil
	}
}

func (event Event) Validate() error {
	hasWorkerAttempt := strings.TrimSpace(event.WorkerAttemptID) != ""
	hasWorkerSequence := event.WorkerEventSequence > 0
	switch {
	case strings.TrimSpace(event.ID) == "":
		return fmt.Errorf("%w: ID is required", ErrInvalidEvent)
	case strings.TrimSpace(event.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidEvent)
	case event.Sequence < 1:
		return fmt.Errorf("%w: sequence must be positive", ErrInvalidEvent)
	case strings.TrimSpace(string(event.Type)) == "":
		return fmt.Errorf("%w: type is required", ErrInvalidEvent)
	case strings.TrimSpace(event.Text) == "":
		return fmt.Errorf("%w: text is required", ErrInvalidEvent)
	case event.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidEvent)
	case event.WorkerEventSequence < 0:
		return fmt.Errorf("%w: worker event sequence cannot be negative", ErrInvalidEvent)
	case hasWorkerAttempt != hasWorkerSequence:
		return fmt.Errorf("%w: worker attempt and event sequence must be provided together", ErrInvalidEvent)
	default:
		return nil
	}
}

func (message PlanningMessage) Validate() error {
	switch {
	case strings.TrimSpace(message.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidPlanningMessage)
	case message.Sequence < 1:
		return fmt.Errorf("%w: sequence must be positive", ErrInvalidPlanningMessage)
	case strings.TrimSpace(message.AgentID) == "":
		return fmt.Errorf("%w: agent ID is required", ErrInvalidPlanningMessage)
	case !message.Role.IsValid():
		return fmt.Errorf("%w: role %q is not recognized", ErrInvalidPlanningMessage, message.Role)
	case message.Event.Type != worker.EventMessage && message.Event.Type != worker.EventPlanSubmitted:
		return fmt.Errorf("%w: referenced event must be an agent message or submitted plan", ErrInvalidPlanningMessage)
	case message.LinkedAt.IsZero():
		return fmt.Errorf("%w: link time is required", ErrInvalidPlanningMessage)
	}
	return message.Event.Validate()
}

func (checkpoint WorkerAttemptCheckpoint) Validate() error {
	switch {
	case strings.TrimSpace(checkpoint.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", ErrInvalidWorkerAttempt)
	case strings.TrimSpace(checkpoint.AttemptID) == "":
		return fmt.Errorf("%w: attempt ID is required", ErrInvalidWorkerAttempt)
	case checkpoint.LastEventSequence < 0:
		return fmt.Errorf("%w: last event sequence cannot be negative", ErrInvalidWorkerAttempt)
	case checkpoint.CreatedAt.IsZero():
		return fmt.Errorf("%w: creation time is required", ErrInvalidWorkerAttempt)
	case checkpoint.UpdatedAt.Before(checkpoint.CreatedAt):
		return fmt.Errorf("%w: update time precedes creation time", ErrInvalidWorkerAttempt)
	default:
		return nil
	}
}

func (status CommandStatus) IsValid() bool {
	return status == CommandStatusPending ||
		status == CommandStatusApplied ||
		status == CommandStatusRejected
}

func (command Command) Validate() error {
	if strings.TrimSpace(command.SessionID) == "" {
		return fmt.Errorf("%w: session ID is required", ErrInvalidCommand)
	}
	if err := (worker.Command{
		ID:      command.ID,
		Type:    command.Type,
		Message: command.Message,
	}).Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	switch {
	case !command.Status.IsValid():
		return fmt.Errorf("%w: status %q is not recognized", ErrInvalidCommand, command.Status)
	case command.RequestedAt.IsZero():
		return fmt.Errorf("%w: request time is required", ErrInvalidCommand)
	case command.Status == CommandStatusPending && command.AppliedAt != nil:
		return fmt.Errorf("%w: pending command cannot have an applied time", ErrInvalidCommand)
	case command.Status == CommandStatusPending && command.Error != "":
		return fmt.Errorf("%w: pending command cannot have an error", ErrInvalidCommand)
	case command.Status != CommandStatusPending && command.AppliedAt == nil:
		return fmt.Errorf("%w: resolved command requires an applied time", ErrInvalidCommand)
	case command.AppliedAt != nil && command.AppliedAt.Before(command.RequestedAt):
		return fmt.Errorf("%w: applied time precedes request time", ErrInvalidCommand)
	case command.Status == CommandStatusApplied && command.Error != "":
		return fmt.Errorf("%w: applied command cannot have an error", ErrInvalidCommand)
	case command.Status == CommandStatusRejected && strings.TrimSpace(command.Error) == "":
		return fmt.Errorf("%w: rejected command requires an error", ErrInvalidCommand)
	default:
		return nil
	}
}
