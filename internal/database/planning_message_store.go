package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func (s *ExecutionStore) LinkPlanningMessage(
	ctx context.Context,
	pending execution.PendingPlanningMessage,
) (execution.PlanningMessage, bool, error) {
	if err := pending.Validate(); err != nil {
		return execution.PlanningMessage{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.PlanningMessage{}, false, fmt.Errorf("begin planning message link: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := findPlanningMessageByEvent(ctx, tx, pending.EventID)
	if err != nil {
		return execution.PlanningMessage{}, false, err
	}
	if found {
		if existing.RunID != pending.RunID {
			return execution.PlanningMessage{}, false, execution.ErrPlanningMessageConflict
		}
		return existing, false, nil
	}

	event, found, err := findExecutionEvent(ctx, tx, pending.EventID)
	if err != nil {
		return execution.PlanningMessage{}, false, err
	}
	if !found {
		return execution.PlanningMessage{}, false, execution.ErrNotFound
	}
	if event.Type != worker.EventMessage && event.Type != worker.EventPlanSubmitted {
		return execution.PlanningMessage{}, false, execution.ErrInvalidPlanningMessage
	}
	var agentID string
	var role worker.Role
	if err := tx.QueryRowContext(
		ctx,
		`SELECT agent_id, role FROM sessions WHERE id = ? AND run_id = ?`,
		event.SessionID, pending.RunID,
	).Scan(&agentID, &role); errors.Is(err, sql.ErrNoRows) {
		return execution.PlanningMessage{}, false, execution.ErrPlanningMessageConflict
	} else if err != nil {
		return execution.PlanningMessage{}, false, fmt.Errorf("verify planning message session: %w", err)
	}

	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM planning_messages WHERE run_id = ?`,
		pending.RunID,
	).Scan(&sequence); err != nil {
		return execution.PlanningMessage{}, false, fmt.Errorf("select next planning message sequence: %w", err)
	}
	message := execution.PlanningMessage{
		RunID: pending.RunID, Sequence: sequence, AgentID: agentID, Role: role,
		Event: event, LinkedAt: pending.LinkedAt.UTC(),
	}
	if err := message.Validate(); err != nil {
		return execution.PlanningMessage{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO planning_messages (run_id, sequence, session_event_id, linked_at)
		 VALUES (?, ?, ?, ?)`,
		message.RunID, message.Sequence, message.Event.ID,
		formatExecutionTime(message.LinkedAt),
	); err != nil {
		return execution.PlanningMessage{}, false, fmt.Errorf("insert planning message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return execution.PlanningMessage{}, false, fmt.Errorf("commit planning message: %w", err)
	}
	return message, true, nil
}

func (s *ExecutionStore) ListPlanningMessages(
	ctx context.Context,
	runID string,
) ([]execution.PlanningMessage, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT pm.run_id, pm.sequence, s.agent_id, s.role,
		        se.id, se.session_id, se.sequence, se.event_type, se.text,
		        se.occurred_at, se.worker_attempt_id, se.worker_event_sequence,
		        pm.linked_at
		 FROM planning_messages pm
		 JOIN session_events se ON se.id = pm.session_event_id
		 JOIN sessions s ON s.id = se.session_id
		 WHERE pm.run_id = ?
		 ORDER BY pm.sequence`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list planning messages for run %q: %w", runID, err)
	}
	defer rows.Close()

	messages := make([]execution.PlanningMessage, 0)
	for rows.Next() {
		message, err := scanPlanningMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate planning messages for run %q: %w", runID, err)
	}
	return messages, nil
}

func findPlanningMessageByEvent(
	ctx context.Context,
	tx *sql.Tx,
	eventID string,
) (execution.PlanningMessage, bool, error) {
	message, err := scanPlanningMessage(tx.QueryRowContext(
		ctx,
		`SELECT pm.run_id, pm.sequence, s.agent_id, s.role,
		        se.id, se.session_id, se.sequence, se.event_type, se.text,
		        se.occurred_at, se.worker_attempt_id, se.worker_event_sequence,
		        pm.linked_at
		 FROM planning_messages pm
		 JOIN session_events se ON se.id = pm.session_event_id
		 JOIN sessions s ON s.id = se.session_id
		 WHERE pm.session_event_id = ?`,
		eventID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.PlanningMessage{}, false, nil
	}
	if err != nil {
		return execution.PlanningMessage{}, false, fmt.Errorf("select planning message by event: %w", err)
	}
	return message, true, nil
}

func scanPlanningMessage(scanner executionScanner) (execution.PlanningMessage, error) {
	message := execution.PlanningMessage{}
	var role string
	var eventType string
	var occurredAt string
	var workerAttemptID sql.NullString
	var workerEventSequence sql.NullInt64
	var linkedAt string
	if err := scanner.Scan(
		&message.RunID, &message.Sequence, &message.AgentID, &role,
		&message.Event.ID, &message.Event.SessionID, &message.Event.Sequence,
		&eventType, &message.Event.Text, &occurredAt,
		&workerAttemptID, &workerEventSequence, &linkedAt,
	); err != nil {
		return execution.PlanningMessage{}, err
	}
	message.Role = worker.Role(role)
	message.Event.Type = worker.EventType(eventType)
	if workerAttemptID.Valid {
		message.Event.WorkerAttemptID = workerAttemptID.String
	}
	if workerEventSequence.Valid {
		message.Event.WorkerEventSequence = workerEventSequence.Int64
	}
	var err error
	message.Event.OccurredAt, err = parseExecutionTime(occurredAt)
	if err != nil {
		return execution.PlanningMessage{}, fmt.Errorf("parse planning event time: %w", err)
	}
	message.LinkedAt, err = parseExecutionTime(linkedAt)
	if err != nil {
		return execution.PlanningMessage{}, fmt.Errorf("parse planning link time: %w", err)
	}
	if err := message.Validate(); err != nil {
		return execution.PlanningMessage{}, err
	}
	return message, nil
}
