package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func (s *ExecutionStore) CreateWorkerAttempt(
	ctx context.Context,
	checkpoint execution.WorkerAttemptCheckpoint,
) (execution.WorkerAttemptCheckpoint, bool, error) {
	if err := checkpoint.Validate(); err != nil {
		return execution.WorkerAttemptCheckpoint{}, false, err
	}
	if checkpoint.LastEventSequence != 0 {
		return execution.WorkerAttemptCheckpoint{}, false, fmt.Errorf(
			"%w: new checkpoint must start before the first event",
			execution.ErrInvalidWorkerAttempt,
		)
	}
	checkpoint.CreatedAt = checkpoint.CreatedAt.UTC()
	checkpoint.UpdatedAt = checkpoint.UpdatedAt.UTC()
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO worker_attempt_checkpoints (
			session_id, attempt_id, last_event_sequence, created_at, updated_at
		 ) VALUES (?, ?, 0, ?, ?)
		 ON CONFLICT(session_id) DO NOTHING`,
		checkpoint.SessionID,
		checkpoint.AttemptID,
		formatExecutionTime(checkpoint.CreatedAt),
		formatExecutionTime(checkpoint.UpdatedAt),
	)
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, false, fmt.Errorf(
			"insert worker attempt %q for session %q: %w",
			checkpoint.AttemptID,
			checkpoint.SessionID,
			err,
		)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, false, fmt.Errorf(
			"read inserted worker attempt %q row count: %w",
			checkpoint.AttemptID,
			err,
		)
	}
	if rowsAffected == 1 {
		return checkpoint, true, nil
	}
	if rowsAffected != 0 {
		return execution.WorkerAttemptCheckpoint{}, false, fmt.Errorf(
			"insert worker attempt %q: expected zero or one affected row, got %d",
			checkpoint.AttemptID,
			rowsAffected,
		)
	}
	existing, err := s.GetWorkerAttempt(ctx, checkpoint.SessionID)
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, false, err
	}
	if existing.AttemptID != checkpoint.AttemptID {
		return execution.WorkerAttemptCheckpoint{}, false, execution.ErrWorkerAttemptConflict
	}
	return existing, false, nil
}

func (s *ExecutionStore) GetWorkerAttempt(
	ctx context.Context,
	sessionID string,
) (execution.WorkerAttemptCheckpoint, error) {
	checkpoint, err := scanWorkerAttemptCheckpoint(s.db.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		sessionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.WorkerAttemptCheckpoint{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, fmt.Errorf(
			"select worker attempt for session %q: %w",
			sessionID,
			err,
		)
	}
	return checkpoint, nil
}

func (s *ExecutionStore) AppendWorkerEvent(
	ctx context.Context,
	pending execution.PendingWorkerEvent,
) (execution.Event, bool, error) {
	if err := pending.Validate(); err != nil {
		return execution.Event{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Event{}, false, fmt.Errorf("begin worker event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		pending.SessionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Event{}, false, execution.ErrNotFound
	}
	if err != nil {
		return execution.Event{}, false, fmt.Errorf(
			"select worker attempt for session %q: %w",
			pending.SessionID,
			err,
		)
	}
	if checkpoint.AttemptID != pending.AttemptID {
		return execution.Event{}, false, execution.ErrWorkerAttemptConflict
	}

	existing, found, err := findWorkerSourceEvent(
		ctx,
		tx,
		pending.SessionID,
		pending.AttemptID,
		pending.SourceSequence,
	)
	if err != nil {
		return execution.Event{}, false, err
	}
	if found {
		if !sameWorkerEvent(existing, pending) ||
			checkpoint.LastEventSequence < pending.SourceSequence {
			return execution.Event{}, false, execution.ErrWorkerEventConflict
		}
		return existing, false, nil
	}
	if pending.SourceSequence != checkpoint.LastEventSequence+1 {
		return execution.Event{}, false, execution.ErrWorkerEventSequence
	}
	if pending.AcceptedAt.Before(checkpoint.CreatedAt) {
		return execution.Event{}, false, fmt.Errorf(
			"%w: event acceptance precedes worker attempt creation",
			execution.ErrInvalidEvent,
		)
	}
	if _, found, err := findExecutionEvent(ctx, tx, pending.ID); err != nil {
		return execution.Event{}, false, err
	} else if found {
		return execution.Event{}, false, execution.ErrEventConflict
	}

	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1
		 FROM session_events WHERE session_id = ?`,
		pending.SessionID,
	).Scan(&sequence); err != nil {
		return execution.Event{}, false, fmt.Errorf(
			"select next event sequence for session %q: %w",
			pending.SessionID,
			err,
		)
	}
	event := execution.Event{
		ID:                  pending.ID,
		SessionID:           pending.SessionID,
		Sequence:            sequence,
		Type:                pending.Type,
		Text:                pending.Text,
		OccurredAt:          pending.OccurredAt.UTC(),
		WorkerAttemptID:     pending.AttemptID,
		WorkerEventSequence: pending.SourceSequence,
	}
	if err := event.Validate(); err != nil {
		return execution.Event{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO session_events (
			id, session_id, sequence, event_type, text, occurred_at,
			worker_attempt_id, worker_event_sequence
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.SessionID,
		event.Sequence,
		event.Type,
		event.Text,
		formatExecutionTime(event.OccurredAt),
		event.WorkerAttemptID,
		event.WorkerEventSequence,
	); err != nil {
		return execution.Event{}, false, fmt.Errorf("insert worker session event %q: %w", event.ID, err)
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE worker_attempt_checkpoints
		 SET last_event_sequence = ?, updated_at = ?
		 WHERE session_id = ? AND attempt_id = ? AND last_event_sequence = ?`,
		pending.SourceSequence,
		formatExecutionTime(pending.AcceptedAt),
		pending.SessionID,
		pending.AttemptID,
		checkpoint.LastEventSequence,
	)
	if err != nil {
		return execution.Event{}, false, fmt.Errorf(
			"advance worker event checkpoint for session %q: %w",
			pending.SessionID,
			err,
		)
	}
	if err := requireExecutionUpdate(result, "worker attempt", pending.AttemptID); err != nil {
		return execution.Event{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Event{}, false, fmt.Errorf("commit worker event %q: %w", event.ID, err)
	}
	return event, true, nil
}

func findWorkerSourceEvent(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	attemptID string,
	sequence int64,
) (execution.Event, bool, error) {
	event, err := scanExecutionEvent(tx.QueryRowContext(
		ctx,
		`SELECT id, session_id, sequence, event_type, text, occurred_at,
		        worker_attempt_id, worker_event_sequence
		 FROM session_events
		 WHERE session_id = ? AND worker_attempt_id = ? AND worker_event_sequence = ?`,
		sessionID,
		attemptID,
		sequence,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Event{}, false, nil
	}
	if err != nil {
		return execution.Event{}, false, fmt.Errorf(
			"select worker event %d for attempt %q: %w",
			sequence,
			attemptID,
			err,
		)
	}
	return event, true, nil
}

func sameWorkerEvent(existing execution.Event, pending execution.PendingWorkerEvent) bool {
	return existing.SessionID == pending.SessionID &&
		existing.WorkerAttemptID == pending.AttemptID &&
		existing.WorkerEventSequence == pending.SourceSequence &&
		existing.Type == pending.Type &&
		existing.Text == pending.Text &&
		existing.OccurredAt.Equal(pending.OccurredAt)
}

func scanWorkerAttemptCheckpoint(
	scanner executionScanner,
) (execution.WorkerAttemptCheckpoint, error) {
	checkpoint := execution.WorkerAttemptCheckpoint{}
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(
		&checkpoint.SessionID,
		&checkpoint.AttemptID,
		&checkpoint.LastEventSequence,
		&createdAt,
		&updatedAt,
	); err != nil {
		return execution.WorkerAttemptCheckpoint{}, err
	}
	var err error
	checkpoint.CreatedAt, err = parseExecutionTime(createdAt)
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, fmt.Errorf(
			"parse worker attempt %q creation time: %w",
			checkpoint.AttemptID,
			err,
		)
	}
	checkpoint.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, fmt.Errorf(
			"parse worker attempt %q update time: %w",
			checkpoint.AttemptID,
			err,
		)
	}
	if err := checkpoint.Validate(); err != nil {
		return execution.WorkerAttemptCheckpoint{}, err
	}
	return checkpoint, nil
}
