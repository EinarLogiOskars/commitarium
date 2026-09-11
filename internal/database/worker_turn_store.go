package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// BeginWorkerTurn records everything that must be true before the coordinator
// may ask a provider to continue an existing conversation. Keeping this in one
// transaction prevents a restart from observing only half of the new turn.
func (s *ExecutionStore) BeginWorkerTurn(
	ctx context.Context,
	admission execution.WorkerTurnAdmission,
) (execution.WorkerTurnAdmissionResult, bool, error) {
	if err := validateWorkerTurnAdmission(admission); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("begin worker turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := findWorkerTurnCommand(ctx, tx, admission.Command.ID)
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}
	if found {
		if existing.SessionID != admission.Command.SessionID ||
			existing.Type != admission.Command.Type ||
			existing.Message != admission.Command.Message {
			return execution.WorkerTurnAdmissionResult{}, false, execution.ErrCommandConflict
		}
		return execution.WorkerTurnAdmissionResult{Command: existing}, false, nil
	}

	session, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		admission.Command.SessionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.WorkerTurnAdmissionResult{}, false, execution.ErrNotFound
	}
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("select session for worker turn: %w", err)
	}
	if session.Status != execution.SessionStatusWaitingForUser || session.ProviderSessionID == "" {
		return execution.WorkerTurnAdmissionResult{}, false, execution.ErrStateConflict
	}
	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		session.RunID,
	))
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("select run for worker turn: %w", err)
	}
	if run.Status != execution.RunStatusWaitingForUser {
		return execution.WorkerTurnAdmissionResult{}, false, execution.ErrStateConflict
	}
	var featureState feature.State
	var acceptedGoal string
	var goalAcceptedAt sql.NullString
	if err := tx.QueryRowContext(
		ctx,
		`SELECT state, accepted_goal, goal_accepted_at FROM features WHERE id = ?`,
		run.FeatureID,
	).Scan(&featureState, &acceptedGoal, &goalAcceptedAt); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("select feature goal boundary: %w", err)
	}
	if featureState != admission.ExpectedFeatureState {
		return execution.WorkerTurnAdmissionResult{}, false, execution.ErrStateConflict
	}
	validGoalBoundary := (featureState == feature.StateDraft &&
		acceptedGoal == "" && !goalAcceptedAt.Valid) ||
		(featureState == feature.StateImplementing &&
			strings.TrimSpace(acceptedGoal) != "" && goalAcceptedAt.Valid)
	if !validGoalBoundary {
		return execution.WorkerTurnAdmissionResult{}, false, execution.ErrStateConflict
	}
	checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		session.ID,
	))
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("select worker checkpoint for new turn: %w", err)
	}
	if checkpoint.AttemptID != admission.PreviousAttemptID ||
		checkpoint.LastEventSequence != admission.PreviousLastEventSequence {
		return execution.WorkerTurnAdmissionResult{}, false, execution.ErrWorkerAttemptConflict
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO session_commands (
			id, session_id, command_type, message, status,
			requested_at, applied_at, error
		 ) VALUES (?, ?, ?, ?, ?, ?, NULL, '')`,
		admission.Command.ID, admission.Command.SessionID, admission.Command.Type,
		admission.Command.Message, admission.Command.Status,
		formatExecutionTime(admission.Command.RequestedAt),
	); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("insert worker turn command: %w", err)
	}

	var eventSequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM session_events WHERE session_id = ?`,
		session.ID,
	).Scan(&eventSequence); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("select worker turn event sequence: %w", err)
	}
	userEvent := execution.Event{
		ID: admission.UserEvent.ID, SessionID: session.ID, Sequence: eventSequence,
		Type: admission.UserEvent.Type, Text: admission.UserEvent.Text,
		OccurredAt: admission.UserEvent.OccurredAt.UTC(),
	}
	if err := userEvent.Validate(); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO session_events (
			id, session_id, sequence, event_type, text, occurred_at,
			worker_attempt_id, worker_event_sequence
		 ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL)`,
		userEvent.ID, userEvent.SessionID, userEvent.Sequence, userEvent.Type,
		userEvent.Text, formatExecutionTime(userEvent.OccurredAt),
	); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("insert worker turn user event: %w", err)
	}

	next := admission.NextAttempt
	next.CreatedAt = next.CreatedAt.UTC()
	next.UpdatedAt = next.UpdatedAt.UTC()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE worker_attempt_checkpoints
		 SET attempt_id = ?, last_event_sequence = 0, created_at = ?, updated_at = ?
		 WHERE session_id = ? AND attempt_id = ? AND last_event_sequence = ?`,
		next.AttemptID, formatExecutionTime(next.CreatedAt), formatExecutionTime(next.UpdatedAt),
		next.SessionID, admission.PreviousAttemptID, admission.PreviousLastEventSequence,
	)
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("replace worker attempt checkpoint: %w", err)
	}
	if err := requireExecutionUpdate(result, "worker attempt", admission.PreviousAttemptID); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}

	session.Status = execution.SessionStatusRunning
	session.UpdatedAt = admission.OccurredAt.UTC()
	if err := session.Validate(); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		session.Status, formatExecutionTime(session.UpdatedAt), session.ID,
		execution.SessionStatusWaitingForUser,
	)
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("activate session for worker turn: %w", err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}

	run.Status = execution.RunStatusRunning
	run.Reason = admission.RunReason
	run.UpdatedAt = admission.OccurredAt.UTC()
	if err := run.Validate(); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE runs SET status = ?, reason = ?, updated_at = ? WHERE id = ? AND status = ?`,
		run.Status, run.Reason, formatExecutionTime(run.UpdatedAt), run.ID,
		execution.RunStatusWaitingForUser,
	)
	if err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("activate run for worker turn: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return execution.WorkerTurnAdmissionResult{}, false, fmt.Errorf("commit worker turn: %w", err)
	}
	return execution.WorkerTurnAdmissionResult{
		Command: admission.Command, UserEvent: userEvent,
	}, true, nil
}

func validateWorkerTurnAdmission(admission execution.WorkerTurnAdmission) error {
	switch {
	case admission.Command.Status != execution.CommandStatusPending:
		return fmt.Errorf("%w: worker turn command must be pending", execution.ErrInvalidCommand)
	case admission.Command.Type != worker.CommandMessage:
		return fmt.Errorf("%w: worker turn requires a message command", execution.ErrInvalidCommand)
	case admission.UserEvent.Type != worker.EventUserMessage:
		return fmt.Errorf("%w: worker turn event must be a user message", execution.ErrInvalidEvent)
	case admission.UserEvent.SessionID != admission.Command.SessionID,
		admission.NextAttempt.SessionID != admission.Command.SessionID:
		return fmt.Errorf("%w: worker turn records must share a session", execution.ErrInvalidWorkerAttempt)
	case admission.UserEvent.Text != admission.Command.Message:
		return fmt.Errorf("%w: worker turn event must preserve the command message", execution.ErrInvalidEvent)
	case strings.TrimSpace(admission.PreviousAttemptID) == "":
		return fmt.Errorf("%w: previous attempt ID is required", execution.ErrInvalidWorkerAttempt)
	case admission.PreviousLastEventSequence < 0:
		return fmt.Errorf("%w: previous event sequence cannot be negative", execution.ErrInvalidWorkerAttempt)
	case admission.NextAttempt.AttemptID == admission.PreviousAttemptID:
		return fmt.Errorf("%w: replacement attempt must be new", execution.ErrInvalidWorkerAttempt)
	case admission.ExpectedFeatureState != feature.StateDraft &&
		admission.ExpectedFeatureState != feature.StateImplementing:
		return fmt.Errorf("%w: worker turn feature state must be draft or implementing", execution.ErrStateConflict)
	case strings.TrimSpace(admission.RunReason) == "":
		return fmt.Errorf("%w: run reason is required", execution.ErrInvalidRun)
	case admission.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", execution.ErrInvalidStatusTransition)
	}
	if err := admission.Command.Validate(); err != nil {
		return err
	}
	if err := admission.UserEvent.Validate(); err != nil {
		return err
	}
	return admission.NextAttempt.Validate()
}

func findWorkerTurnCommand(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (execution.Command, bool, error) {
	command, err := scanExecutionCommand(tx.QueryRowContext(
		ctx,
		`SELECT id, session_id, command_type, message, status,
		        requested_at, applied_at, error
		 FROM session_commands WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Command{}, false, nil
	}
	if err != nil {
		return execution.Command{}, false, fmt.Errorf("select worker turn command: %w", err)
	}
	return command, true, nil
}
