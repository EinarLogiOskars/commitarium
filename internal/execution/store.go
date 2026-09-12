package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
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
	WaitKind   RunWaitKind
	OccurredAt time.Time
}

type RunPauseAction string

const (
	RunPauseActionPause  RunPauseAction = "pause"
	RunPauseActionResume RunPauseAction = "resume"
)

type RunPauseMutation struct {
	ID         string
	RunID      string
	Action     RunPauseAction
	OccurredAt time.Time
}

// InterventionRequest is the atomic user action that records a message and
// arms the existing run pause gate. SessionID is resolved by the store from
// Target so callers cannot accidentally address a session from another run.
type InterventionRequest struct {
	ID         string
	RunID      string
	Target     worker.Role
	Message    string
	OccurredAt time.Time
}

type InterventionRequestResult struct {
	Intervention Intervention
	UserEvent    Event
}

// InterventionTurnAdmission binds one queued message to one deterministic
// resume attempt. Unlike an autonomous workflow turn, the run deliberately
// remains paused and waiting while the selected conversation answers.
type InterventionTurnAdmission struct {
	InterventionID            string
	PreviousAttemptID         string
	PreviousLastEventSequence int64
	NextAttempt               WorkerAttemptCheckpoint
	RunReason                 string
	OccurredAt                time.Time
}

type InterventionCompletion struct {
	InterventionID    string
	AttemptID         string
	ProviderSessionID string
	Effect            worker.InterventionEffect
	RunReason         string
	OccurredAt        time.Time
}

// InterventionGuidanceResolution consumes one answered guidance result and
// releases the run's pause gate in the same transaction. The ordinary run
// pause action ID provides the public idempotency boundary.
type InterventionGuidanceResolution struct {
	ID             string
	RunID          string
	InterventionID string
	OccurredAt     time.Time
}

// ReplanningTurnAdmission is the durable handoff from an answered material
// intervention to the lead's first turn for the next plan version.
type ReplanningTurnAdmission struct {
	ID                        string
	RunID                     string
	InterventionID            string
	PlanVersion               int
	PreviousPlanEventID       string
	EffectiveGoal             string
	BaselineCommitID          string
	PreviousAttemptID         string
	PreviousLastEventSequence int64
	NextAttempt               WorkerAttemptCheckpoint
	RunReason                 string
	OccurredAt                time.Time
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
	ExpectedFeatureState      feature.State
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
	ExpectedFeatureState      feature.State
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
	ListRunsByFeatureID(ctx context.Context, featureID string) ([]Run, error)
	TransitionRun(ctx context.Context, transition RunTransition) (Run, error)
	ApplyRunPause(ctx context.Context, mutation RunPauseMutation) (Run, bool, error)
	QueueIntervention(ctx context.Context, request InterventionRequest) (InterventionRequestResult, bool, error)
	GetLatestIntervention(ctx context.Context, runID string) (Intervention, error)
	ListInterventionTargets(ctx context.Context, runID string) ([]InterventionTarget, error)
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
	BeginInterventionTurn(ctx context.Context, admission InterventionTurnAdmission) (bool, error)
	CompleteIntervention(ctx context.Context, completion InterventionCompletion) (Intervention, bool, error)
	ResolveInterventionGuidance(ctx context.Context, resolution InterventionGuidanceResolution) (Run, bool, error)
	BeginReplanningTurn(ctx context.Context, admission ReplanningTurnAdmission) (Run, PlanRevision, bool, error)
	GetPlanRevision(ctx context.Context, runID string, version int) (PlanRevision, error)
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
var ErrRunActionConflict = errors.New("run action ID reused for a different operation")
var ErrInterventionConflict = errors.New("intervention ID reused for different content")
var ErrInterventionInProgress = errors.New("run already has an unfinished intervention")
var ErrInterventionNotAllowed = errors.New("run does not allow an intervention")
var ErrInterventionTargetUnavailable = errors.New("intervention target is unavailable")

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
	reclassifiesWait := transition.Expected == RunStatusWaitingForUser &&
		transition.Status == RunStatusWaitingForUser
	switch {
	case strings.TrimSpace(transition.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidStatusTransition)
	case !reclassifiesWait && !transition.Expected.CanTransitionTo(transition.Status):
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

func (mutation RunPauseMutation) Validate() error {
	switch {
	case strings.TrimSpace(mutation.ID) == "":
		return fmt.Errorf("%w: action ID is required", ErrInvalidCommand)
	case strings.TrimSpace(mutation.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidCommand)
	case mutation.Action != RunPauseActionPause && mutation.Action != RunPauseActionResume:
		return fmt.Errorf("%w: run pause action is invalid", ErrInvalidCommand)
	case mutation.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidCommand)
	default:
		return nil
	}
}

func (request InterventionRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.ID) == "":
		return fmt.Errorf("%w: intervention ID is required", ErrInvalidIntervention)
	case strings.TrimSpace(request.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidIntervention)
	case request.Target != worker.RoleLead && request.Target != worker.RoleReviewer:
		return fmt.Errorf("%w: target must be lead or reviewer", ErrInvalidIntervention)
	case strings.TrimSpace(request.Message) == "":
		return fmt.Errorf("%w: message is required", ErrInvalidIntervention)
	case request.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidIntervention)
	default:
		return nil
	}
}

func (admission InterventionTurnAdmission) Validate() error {
	switch {
	case strings.TrimSpace(admission.InterventionID) == "":
		return fmt.Errorf("%w: intervention ID is required", ErrInvalidIntervention)
	case strings.TrimSpace(admission.PreviousAttemptID) == "":
		return fmt.Errorf("%w: previous attempt ID is required", ErrInvalidWorkerAttempt)
	case admission.PreviousLastEventSequence < 0:
		return fmt.Errorf("%w: previous event sequence cannot be negative", ErrInvalidWorkerAttempt)
	case admission.NextAttempt.AttemptID == admission.PreviousAttemptID:
		return fmt.Errorf("%w: replacement attempt must be new", ErrInvalidWorkerAttempt)
	case strings.TrimSpace(admission.RunReason) == "":
		return fmt.Errorf("%w: run reason is required", ErrInvalidRun)
	case admission.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidStatusTransition)
	}
	return admission.NextAttempt.Validate()
}

func (completion InterventionCompletion) Validate() error {
	switch {
	case strings.TrimSpace(completion.InterventionID) == "":
		return fmt.Errorf("%w: intervention ID is required", ErrInvalidIntervention)
	case strings.TrimSpace(completion.AttemptID) == "":
		return fmt.Errorf("%w: attempt ID is required", ErrInvalidWorkerAttempt)
	case strings.TrimSpace(completion.ProviderSessionID) == "":
		return fmt.Errorf("%w: provider session ID is required", ErrInvalidSession)
	case !completion.Effect.IsValid():
		return fmt.Errorf("%w: intervention effect is invalid", ErrInvalidIntervention)
	case strings.TrimSpace(completion.RunReason) == "":
		return fmt.Errorf("%w: run reason is required", ErrInvalidRun)
	case completion.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidStatusTransition)
	default:
		return nil
	}
}

func (resolution InterventionGuidanceResolution) Validate() error {
	switch {
	case strings.TrimSpace(resolution.ID) == "":
		return fmt.Errorf("%w: action ID is required", ErrInvalidCommand)
	case strings.TrimSpace(resolution.RunID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidIntervention)
	case strings.TrimSpace(resolution.InterventionID) == "":
		return fmt.Errorf("%w: intervention ID is required", ErrInvalidIntervention)
	case resolution.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", ErrInvalidStatusTransition)
	default:
		return nil
	}
}

func (admission ReplanningTurnAdmission) Validate() error {
	revision := PlanRevision{
		RunID: admission.RunID, Version: admission.PlanVersion, InterventionID: admission.InterventionID,
		PreviousPlanEventID: admission.PreviousPlanEventID,
		EffectiveGoal:       admission.EffectiveGoal, BaselineCommitID: admission.BaselineCommitID,
		CreatedAt: admission.OccurredAt,
	}
	if err := revision.Validate(); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(admission.ID) == "":
		return fmt.Errorf("%w: action ID is required", ErrInvalidCommand)
	case strings.TrimSpace(admission.PreviousAttemptID) == "":
		return fmt.Errorf("%w: previous attempt ID is required", ErrInvalidWorkerAttempt)
	case admission.PreviousLastEventSequence < 0:
		return fmt.Errorf("%w: previous event sequence cannot be negative", ErrInvalidWorkerAttempt)
	case admission.NextAttempt.SessionID == "":
		return fmt.Errorf("%w: next attempt session is required", ErrInvalidWorkerAttempt)
	case admission.NextAttempt.AttemptID == admission.PreviousAttemptID:
		return fmt.Errorf("%w: replacement attempt must be new", ErrInvalidWorkerAttempt)
	case strings.TrimSpace(admission.RunReason) == "":
		return fmt.Errorf("%w: run reason is required", ErrInvalidRun)
	}
	return admission.NextAttempt.Validate()
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
