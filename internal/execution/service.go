package execution

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type Service struct {
	store      Store
	generateID func() string
	now        func() time.Time
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
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
) (Run, bool, error) {
	now := s.now().UTC()
	run := Run{
		ID: id, FeatureID: featureID, Status: RunStatusRunning,
		StartedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateRun(ctx, run); err != nil {
		if !errors.Is(err, ErrAlreadyExists) {
			return Run{}, false, fmt.Errorf("create run %q: %w", id, err)
		}
		existing, getErr := s.store.GetRun(ctx, id)
		if getErr != nil {
			return Run{}, false, fmt.Errorf("get existing run %q: %w", id, getErr)
		}
		if existing.FeatureID != featureID {
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
	recorded, err := s.store.AppendEvent(ctx, PendingEvent{
		ID: id, SessionID: sessionID,
		Type: event.Type, Text: event.Text, OccurredAt: s.now().UTC(),
	})
	if err != nil {
		return Event{}, fmt.Errorf("record event for session %q: %w", sessionID, err)
	}
	return recorded, nil
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

func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	return s.store.GetRun(ctx, id)
}

func (s *Service) GetSession(ctx context.Context, id string) (Session, error) {
	return s.store.GetSession(ctx, id)
}

func (s *Service) EventsForSession(
	ctx context.Context,
	sessionID string,
) ([]Event, error) {
	return s.store.ListEvents(ctx, sessionID)
}
