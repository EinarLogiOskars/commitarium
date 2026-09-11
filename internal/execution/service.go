package execution

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type Service struct {
	store      Store
	broker     *eventBroker
	planning   *planningMessageBroker
	generateID func() string
	now        func() time.Time
}

func NewService(store Store) *Service {
	return &Service{
		store:    store,
		broker:   newEventBroker(defaultSubscriberBuffer),
		planning: newPlanningMessageBroker(defaultSubscriberBuffer),
		generateID: func() string {
			return "sev_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) CreateRun(
	ctx context.Context,
	id string,
	featureID string,
	planningRoundLimit int,
	implementationReviewRoundLimit int,
	agentProviders project.AgentProviders,
) (Run, bool, error) {
	agentProviders, err := agentProviders.Normalize()
	if err != nil {
		return Run{}, false, err
	}
	now := s.now().UTC()
	run := Run{
		ID: id, FeatureID: featureID, Status: RunStatusRunning,
		PlanningRoundLimit:             planningRoundLimit,
		ImplementationReviewRoundLimit: implementationReviewRoundLimit,
		AgentProviders:                 agentProviders,
		StartedAt:                      now, UpdatedAt: now,
	}
	if err := s.store.CreateRun(ctx, run); err != nil {
		if !errors.Is(err, ErrAlreadyExists) {
			return Run{}, false, fmt.Errorf("create run %q: %w", id, err)
		}
		existing, getErr := s.store.GetRun(ctx, id)
		if getErr != nil {
			return Run{}, false, fmt.Errorf("get existing run %q: %w", id, getErr)
		}
		existingProviders, providerErr := existing.AgentProviders.Normalize()
		if providerErr != nil {
			return Run{}, false, providerErr
		}
		if existing.FeatureID != featureID ||
			existing.PlanningRoundLimit != planningRoundLimit ||
			existing.ImplementationReviewRoundLimit != implementationReviewRoundLimit ||
			existingProviders != agentProviders {
			return Run{}, false, ErrRecordConflict
		}
		return existing, false, nil
	}
	return run, true, nil
}

func (s *Service) CreateSession(
	ctx context.Context,
	id string,
	runID string,
	agentID string,
	role worker.Role,
) (Session, bool, error) {
	now := s.now().UTC()
	session := Session{
		ID: id, RunID: runID, AgentID: agentID, Role: role,
		Status: SessionStatusStarting, StartedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		if !errors.Is(err, ErrAlreadyExists) {
			return Session{}, false, fmt.Errorf("create session %q: %w", id, err)
		}
		existing, getErr := s.store.GetSession(ctx, id)
		if getErr != nil {
			return Session{}, false, fmt.Errorf("get existing session %q: %w", id, getErr)
		}
		if existing.RunID != runID ||
			existing.AgentID != agentID ||
			existing.Role != role {
			return Session{}, false, ErrRecordConflict
		}
		return existing, false, nil
	}
	return session, true, nil
}

func (s *Service) TransitionRun(
	ctx context.Context,
	id string,
	expected RunStatus,
	status RunStatus,
	reason string,
) (Run, error) {
	run, err := s.store.TransitionRun(ctx, RunTransition{
		RunID: id, Expected: expected, Status: status,
		Reason: reason, OccurredAt: s.now().UTC(),
	})
	if err != nil {
		return Run{}, fmt.Errorf("transition run %q to %q: %w", id, status, err)
	}
	return run, nil
}

func (s *Service) TransitionSession(
	ctx context.Context,
	id string,
	expected SessionStatus,
	status SessionStatus,
	providerSessionID string,
) (Session, error) {
	session, err := s.store.TransitionSession(ctx, SessionTransition{
		SessionID: id, Expected: expected, Status: status,
		ProviderSessionID: providerSessionID, OccurredAt: s.now().UTC(),
	})
	if err != nil {
		return Session{}, fmt.Errorf("transition session %q to %q: %w", id, status, err)
	}
	return session, nil
}

func (s *Service) CompleteSession(
	ctx context.Context,
	id string,
	expected SessionStatus,
	status SessionStatus,
	result worker.Result,
) (Session, error) {
	session, err := s.store.TransitionSession(ctx, SessionTransition{
		SessionID: id, Expected: expected, Status: status,
		ProviderSessionID: result.ProviderSessionID,
		Result:            &result,
		OccurredAt:        s.now().UTC(),
	})
	if err != nil {
		return Session{}, fmt.Errorf("complete session %q as %q: %w", id, status, err)
	}
	return session, nil
}

func (s *Service) BeginSessionRecovery(
	ctx context.Context,
	id string,
	expected SessionStatus,
) (Session, error) {
	session, err := s.store.BeginSessionRecovery(ctx, SessionRecovery{
		SessionID: id, Expected: expected, OccurredAt: s.now().UTC(),
	})
	if err != nil {
		return Session{}, fmt.Errorf("begin recovery for session %q: %w", id, err)
	}
	return session, nil
}

func (s *Service) RecordSessionEvent(
	ctx context.Context,
	sessionID string,
	event worker.Event,
) (Event, error) {
	return s.RecordSessionEventWithID(ctx, s.generateID(), sessionID, event)
}

// RecordSessionEventWithID lets a replayable event source supply a stable ID.
// Reusing that ID with the same content is an idempotent retry at the store.
func (s *Service) RecordSessionEventWithID(
	ctx context.Context,
	id string,
	sessionID string,
	event worker.Event,
) (Event, error) {
	recorded, created, err := s.store.AppendEvent(ctx, PendingEvent{
		ID: id, SessionID: sessionID,
		Type: event.Type, Text: event.Text, OccurredAt: s.now().UTC(),
	})
	if err != nil {
		return Event{}, fmt.Errorf("record event for session %q: %w", sessionID, err)
	}
	if created && s.broker != nil {
		s.broker.publish(recorded)
	}
	return recorded, nil
}

// CreateWorkerAttempt binds a coordinator session to one concrete worker
// process incarnation. Retrying the same binding is safe; a different attempt
// remains fenced until a future recovery operation explicitly replaces it.
func (s *Service) CreateWorkerAttempt(
	ctx context.Context,
	sessionID string,
	attemptID string,
) (WorkerAttemptCheckpoint, bool, error) {
	now := s.now().UTC()
	checkpoint, created, err := s.store.CreateWorkerAttempt(ctx, WorkerAttemptCheckpoint{
		SessionID: sessionID,
		AttemptID: attemptID,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return WorkerAttemptCheckpoint{}, false, fmt.Errorf(
			"create worker attempt %q for session %q: %w",
			attemptID,
			sessionID,
			err,
		)
	}
	return checkpoint, created, nil
}

func (s *Service) GetWorkerAttempt(
	ctx context.Context,
	sessionID string,
) (WorkerAttemptCheckpoint, error) {
	checkpoint, err := s.store.GetWorkerAttempt(ctx, sessionID)
	if err != nil {
		return WorkerAttemptCheckpoint{}, fmt.Errorf(
			"get worker attempt for session %q: %w",
			sessionID,
			err,
		)
	}
	return checkpoint, nil
}

// RecordWorkerEvent stores a worker event and advances its replay cursor as one
// database operation. Publication happens only after that operation commits.
func (s *Service) RecordWorkerEvent(
	ctx context.Context,
	sessionID string,
	attemptID string,
	sourceSequence int64,
	occurredAt time.Time,
	event worker.Event,
) (Event, bool, error) {
	recorded, created, err := s.store.AppendWorkerEvent(ctx, PendingWorkerEvent{
		ID:             s.generateID(),
		SessionID:      sessionID,
		AttemptID:      attemptID,
		SourceSequence: sourceSequence,
		Type:           event.Type,
		Text:           event.Text,
		OccurredAt:     occurredAt,
		AcceptedAt:     s.now().UTC(),
	})
	if err != nil {
		return Event{}, false, fmt.Errorf(
			"record worker event %d for session %q: %w",
			sourceSequence,
			sessionID,
			err,
		)
	}
	if created && s.broker != nil {
		s.broker.publish(recorded)
	}
	return recorded, created, nil
}

func (s *Service) CreateCommand(
	ctx context.Context,
	id string,
	sessionID string,
	commandType worker.CommandType,
	message string,
) (Command, bool, error) {
	command, created, err := s.store.CreateCommand(ctx, Command{
		ID: id, SessionID: sessionID, Type: commandType, Message: message,
		Status: CommandStatusPending, RequestedAt: s.now().UTC(),
	})
	if err != nil {
		return Command{}, false, fmt.Errorf("create command %q: %w", id, err)
	}
	return command, created, nil
}

func (s *Service) ResolveCommand(
	ctx context.Context,
	id string,
	status CommandStatus,
	errorMessage string,
) (Command, error) {
	command, err := s.store.ResolveCommand(ctx, CommandResolution{
		CommandID: id, Status: status,
		AppliedAt: s.now().UTC(), Error: errorMessage,
	})
	if err != nil {
		return Command{}, fmt.Errorf("resolve command %q: %w", id, err)
	}
	return command, nil
}

func (s *Service) GetCommand(ctx context.Context, id string) (Command, error) {
	return s.store.GetCommand(ctx, id)
}

// BeginWorkerTurn makes a user's reply and the coordinator state needed to
// deliver it one atomic database operation. Publication happens only after
// that operation commits, just as it does for ordinary session events.
func (s *Service) BeginWorkerTurn(
	ctx context.Context,
	requested worker.Command,
	sessionID string,
	previous WorkerAttemptCheckpoint,
	nextAttemptID string,
	expectedFeatureState feature.State,
	runReason string,
) (WorkerTurnAdmissionResult, bool, error) {
	now := s.now().UTC()
	command := Command{
		ID: requested.ID, SessionID: sessionID, Type: requested.Type,
		Message: requested.Message, Status: CommandStatusPending, RequestedAt: now,
	}
	result, admitted, err := s.store.BeginWorkerTurn(ctx, WorkerTurnAdmission{
		Command: command,
		UserEvent: PendingEvent{
			ID: command.ID + ":user-message", SessionID: command.SessionID,
			Type: worker.EventUserMessage, Text: command.Message, OccurredAt: now,
		},
		PreviousAttemptID:         previous.AttemptID,
		PreviousLastEventSequence: previous.LastEventSequence,
		NextAttempt: WorkerAttemptCheckpoint{
			SessionID: command.SessionID, AttemptID: nextAttemptID,
			CreatedAt: now, UpdatedAt: now,
		},
		ExpectedFeatureState: expectedFeatureState,
		RunReason:            runReason,
		OccurredAt:           now,
	})
	if err != nil {
		return WorkerTurnAdmissionResult{}, false, fmt.Errorf(
			"begin worker turn for session %q: %w", command.SessionID, err,
		)
	}
	if admitted && s.broker != nil {
		s.broker.publish(result.UserEvent)
	}
	return result, admitted, nil
}

// BeginAutonomousTurn admits a coordinator-started turn without inventing a
// user command. It is used when workflow policy, rather than a new user
// message, tells an existing provider conversation to continue.
func (s *Service) BeginAutonomousTurn(
	ctx context.Context,
	sessionID string,
	previous WorkerAttemptCheckpoint,
	nextAttemptID string,
	expectedFeatureState feature.State,
	runReason string,
) (bool, error) {
	now := s.now().UTC()
	admitted, err := s.store.BeginAutonomousTurn(ctx, AutonomousTurnAdmission{
		SessionID:                 sessionID,
		PreviousAttemptID:         previous.AttemptID,
		PreviousLastEventSequence: previous.LastEventSequence,
		NextAttempt: WorkerAttemptCheckpoint{
			SessionID: sessionID, AttemptID: nextAttemptID,
			CreatedAt: now, UpdatedAt: now,
		},
		ExpectedFeatureState: expectedFeatureState,
		RunReason:            runReason,
		OccurredAt:           now,
	})
	if err != nil {
		return false, fmt.Errorf("begin autonomous turn for session %q: %w", sessionID, err)
	}
	return admitted, nil
}

// BeginChainedTurn starts the next agent turn while the overall run remains
// active. The store requires every other session to be waiting or terminal,
// which prevents the handoff from creating two concurrently working agents.
func (s *Service) BeginChainedTurn(
	ctx context.Context,
	sessionID string,
	previous WorkerAttemptCheckpoint,
	nextAttemptID string,
	runReason string,
) (bool, error) {
	now := s.now().UTC()
	admitted, err := s.store.BeginAutonomousTurn(ctx, AutonomousTurnAdmission{
		SessionID:                 sessionID,
		PreviousAttemptID:         previous.AttemptID,
		PreviousLastEventSequence: previous.LastEventSequence,
		NextAttempt: WorkerAttemptCheckpoint{
			SessionID: sessionID, AttemptID: nextAttemptID,
			CreatedAt: now, UpdatedAt: now,
		},
		ExpectedFeatureState: feature.StatePlanning,
		RunReason:            runReason,
		OccurredAt:           now,
		RunAlreadyActive:     true,
	})
	if err != nil {
		return false, fmt.Errorf("begin chained turn for session %q: %w", sessionID, err)
	}
	return admitted, nil
}

// BeginChainedTurnInState hands an already-running workflow to another agent
// while requiring the durable feature phase chosen by the caller. It is used
// for cross-phase handoffs such as implementation to independent review.
func (s *Service) BeginChainedTurnInState(
	ctx context.Context,
	sessionID string,
	previous WorkerAttemptCheckpoint,
	nextAttemptID string,
	expectedFeatureState feature.State,
	runReason string,
) (bool, error) {
	now := s.now().UTC()
	admitted, err := s.store.BeginAutonomousTurn(ctx, AutonomousTurnAdmission{
		SessionID:                 sessionID,
		PreviousAttemptID:         previous.AttemptID,
		PreviousLastEventSequence: previous.LastEventSequence,
		NextAttempt: WorkerAttemptCheckpoint{
			SessionID: sessionID, AttemptID: nextAttemptID,
			CreatedAt: now, UpdatedAt: now,
		},
		ExpectedFeatureState: expectedFeatureState,
		RunReason:            runReason,
		OccurredAt:           now,
		RunAlreadyActive:     true,
	})
	if err != nil {
		return false, fmt.Errorf("begin chained turn for session %q: %w", sessionID, err)
	}
	return admitted, nil
}

// BeginNewSessionTurn creates the first attempt of a second logical agent
// conversation without leaving partially admitted work across a restart.
func (s *Service) BeginNewSessionTurn(
	ctx context.Context,
	sessionID string,
	runID string,
	agentID string,
	role worker.Role,
	attemptID string,
	runReason string,
) (bool, error) {
	now := s.now().UTC()
	admitted, err := s.store.BeginNewSessionTurn(ctx, NewSessionTurnAdmission{
		Session: Session{
			ID: sessionID, RunID: runID, AgentID: agentID, Role: role,
			Status: SessionStatusStarting, StartedAt: now, UpdatedAt: now,
		},
		Attempt: WorkerAttemptCheckpoint{
			SessionID: sessionID, AttemptID: attemptID,
			CreatedAt: now, UpdatedAt: now,
		},
		RunReason: runReason, OccurredAt: now,
	})
	if err != nil {
		return false, fmt.Errorf("begin new session turn %q: %w", sessionID, err)
	}
	return admitted, nil
}

func (s *Service) LinkPlanningMessage(
	ctx context.Context,
	runID string,
	eventID string,
) (PlanningMessage, bool, error) {
	message, created, err := s.store.LinkPlanningMessage(ctx, PendingPlanningMessage{
		RunID: runID, EventID: eventID, LinkedAt: s.now().UTC(),
	})
	if err != nil {
		return PlanningMessage{}, false, fmt.Errorf("link planning message %q: %w", eventID, err)
	}
	if created && s.planning != nil {
		s.planning.publish(message)
	}
	return message, created, nil
}

func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	return s.store.GetRun(ctx, id)
}

func (s *Service) RunsForFeature(
	ctx context.Context,
	featureID string,
) ([]Run, error) {
	return s.store.ListRunsByFeatureID(ctx, featureID)
}

func (s *Service) GetSession(ctx context.Context, id string) (Session, error) {
	return s.store.GetSession(ctx, id)
}

func (s *Service) SessionsForRun(
	ctx context.Context,
	runID string,
) ([]Session, error) {
	return s.store.ListSessions(ctx, runID)
}

func (s *Service) RecoverableRuns(ctx context.Context) ([]Run, error) {
	return s.store.ListRecoverableRuns(ctx)
}

func (s *Service) ActiveSessionsForRun(
	ctx context.Context,
	runID string,
) ([]Session, error) {
	return s.store.ListActiveSessions(ctx, runID)
}

func (s *Service) EventsForSession(
	ctx context.Context,
	sessionID string,
) ([]Event, error) {
	return s.store.ListEvents(ctx, sessionID)
}

func (s *Service) PlanningMessagesForRun(
	ctx context.Context,
	runID string,
) ([]PlanningMessage, error) {
	return s.store.ListPlanningMessages(ctx, runID)
}

func (s *Service) SubscribePlanningMessages(
	runID string,
) (<-chan PlanningMessage, func()) {
	return s.planning.subscribe(runID)
}

func (s *Service) PendingCommandsForSession(
	ctx context.Context,
	sessionID string,
) ([]Command, error) {
	return s.store.ListPendingCommands(ctx, sessionID)
}

func (s *Service) SubscribeSessionEvents(
	sessionID string,
) (<-chan Event, func()) {
	return s.broker.subscribe(sessionID)
}
