package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type ExecutionStore struct {
	db *sql.DB
}

var _ execution.Store = (*ExecutionStore)(nil)

func NewExecutionStore(db *sql.DB) *ExecutionStore {
	return &ExecutionStore{db: db}
}

func (s *ExecutionStore) CreateRun(ctx context.Context, run execution.Run) error {
	if err := run.Validate(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO runs (
			id, feature_id, status, reason, started_at, updated_at, ended_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		run.ID,
		run.FeatureID,
		run.Status,
		run.Reason,
		formatExecutionTime(run.StartedAt),
		formatExecutionTime(run.UpdatedAt),
		formatOptionalExecutionTime(run.EndedAt),
	)
	if err != nil {
		return fmt.Errorf("insert run %q: %w", run.ID, err)
	}
	return requireExecutionInsert(result, "run", run.ID)
}

func (s *ExecutionStore) GetRun(
	ctx context.Context,
	id string,
) (execution.Run, error) {
	run, err := scanExecutionRun(s.db.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason, started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Run{}, fmt.Errorf("select run %q: %w", id, err)
	}
	return run, nil
}

func (s *ExecutionStore) CreateSession(
	ctx context.Context,
	session execution.Session,
) error {
	if err := session.Validate(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO sessions (
			id, run_id, agent_id, role, status, provider_session_id,
			started_at, updated_at, ended_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		session.ID,
		session.RunID,
		session.AgentID,
		session.Role,
		session.Status,
		session.ProviderSessionID,
		formatExecutionTime(session.StartedAt),
		formatExecutionTime(session.UpdatedAt),
		formatOptionalExecutionTime(session.EndedAt),
	)
	if err != nil {
		return fmt.Errorf("insert session %q: %w", session.ID, err)
	}
	return requireExecutionInsert(result, "session", session.ID)
}

func (s *ExecutionStore) GetSession(
	ctx context.Context,
	id string,
) (execution.Session, error) {
	session, err := scanExecutionSession(s.db.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Session{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Session{}, fmt.Errorf("select session %q: %w", id, err)
	}
	return session, nil
}

func (s *ExecutionStore) AppendEvent(
	ctx context.Context,
	pending execution.PendingEvent,
) (execution.Event, error) {
	if err := pending.Validate(); err != nil {
		return execution.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Event{}, fmt.Errorf("begin session event: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	existing, found, err := findExecutionEvent(ctx, tx, pending.ID)
	if err != nil {
		return execution.Event{}, err
	}
	if found {
		if existing.SessionID != pending.SessionID ||
			existing.Type != pending.Type ||
			existing.Text != pending.Text ||
			!existing.OccurredAt.Equal(pending.OccurredAt) {
			return execution.Event{}, execution.ErrEventConflict
		}
		return existing, nil
	}

	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1
		 FROM session_events WHERE session_id = ?`,
		pending.SessionID,
	).Scan(&sequence); err != nil {
		return execution.Event{}, fmt.Errorf(
			"select next event sequence for session %q: %w",
			pending.SessionID,
			err,
		)
	}
	event := execution.Event{
		ID:         pending.ID,
		SessionID:  pending.SessionID,
		Sequence:   sequence,
		Type:       pending.Type,
		Text:       pending.Text,
		OccurredAt: pending.OccurredAt.UTC(),
	}
	if err := event.Validate(); err != nil {
		return execution.Event{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO session_events (
			id, session_id, sequence, event_type, text, occurred_at
		 ) VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.SessionID,
		event.Sequence,
		event.Type,
		event.Text,
		formatExecutionTime(event.OccurredAt),
	); err != nil {
		return execution.Event{}, fmt.Errorf("insert session event %q: %w", event.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Event{}, fmt.Errorf("commit session event %q: %w", event.ID, err)
	}
	return event, nil
}

func (s *ExecutionStore) ListEvents(
	ctx context.Context,
	sessionID string,
) ([]execution.Event, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, session_id, sequence, event_type, text, occurred_at
		 FROM session_events WHERE session_id = ? ORDER BY sequence`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list events for session %q: %w", sessionID, err)
	}
	defer rows.Close()

	events := make([]execution.Event, 0)
	for rows.Next() {
		event, err := scanExecutionEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events for session %q: %w", sessionID, err)
	}
	return events, nil
}

func (s *ExecutionStore) CreateCommand(
	ctx context.Context,
	command execution.Command,
) (execution.Command, error) {
	if err := command.Validate(); err != nil {
		return execution.Command{}, err
	}
	if command.Status != execution.CommandStatusPending {
		return execution.Command{}, fmt.Errorf(
			"%w: new command must be pending",
			execution.ErrInvalidCommand,
		)
	}
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO session_commands (
			id, session_id, command_type, message, status,
			requested_at, applied_at, error
		 ) VALUES (?, ?, ?, ?, ?, ?, NULL, '')
		 ON CONFLICT(id) DO NOTHING`,
		command.ID,
		command.SessionID,
		command.Type,
		command.Message,
		command.Status,
		formatExecutionTime(command.RequestedAt),
	)
	if err != nil {
		return execution.Command{}, fmt.Errorf("insert session command %q: %w", command.ID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return execution.Command{}, fmt.Errorf("read inserted command %q row count: %w", command.ID, err)
	}
	if rowsAffected == 1 {
		return command, nil
	}
	if rowsAffected != 0 {
		return execution.Command{}, fmt.Errorf(
			"insert session command %q: expected zero or one affected row, got %d",
			command.ID,
			rowsAffected,
		)
	}
	existing, err := s.GetCommand(ctx, command.ID)
	if err != nil {
		return execution.Command{}, err
	}
	if existing.SessionID != command.SessionID ||
		existing.Type != command.Type ||
		existing.Message != command.Message {
		return execution.Command{}, execution.ErrCommandConflict
	}
	return existing, nil
}

func (s *ExecutionStore) GetCommand(
	ctx context.Context,
	id string,
) (execution.Command, error) {
	command, err := scanExecutionCommand(s.db.QueryRowContext(
		ctx,
		`SELECT id, session_id, command_type, message, status,
		        requested_at, applied_at, error
		 FROM session_commands WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Command{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Command{}, fmt.Errorf("select session command %q: %w", id, err)
	}
	return command, nil
}

type executionScanner interface {
	Scan(dest ...any) error
}

func scanExecutionRun(scanner executionScanner) (execution.Run, error) {
	run := execution.Run{}
	var status string
	var startedAt string
	var updatedAt string
	var endedAt sql.NullString
	if err := scanner.Scan(
		&run.ID,
		&run.FeatureID,
		&status,
		&run.Reason,
		&startedAt,
		&updatedAt,
		&endedAt,
	); err != nil {
		return execution.Run{}, err
	}
	run.Status = execution.RunStatus(status)
	var err error
	run.StartedAt, err = parseExecutionTime(startedAt)
	if err != nil {
		return execution.Run{}, fmt.Errorf("parse run %q start time: %w", run.ID, err)
	}
	run.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return execution.Run{}, fmt.Errorf("parse run %q update time: %w", run.ID, err)
	}
	run.EndedAt, err = parseOptionalExecutionTime(endedAt)
	if err != nil {
		return execution.Run{}, fmt.Errorf("parse run %q end time: %w", run.ID, err)
	}
	if err := run.Validate(); err != nil {
		return execution.Run{}, err
	}
	return run, nil
}

func scanExecutionSession(scanner executionScanner) (execution.Session, error) {
	session := execution.Session{}
	var role string
	var status string
	var startedAt string
	var updatedAt string
	var endedAt sql.NullString
	if err := scanner.Scan(
		&session.ID,
		&session.RunID,
		&session.AgentID,
		&role,
		&status,
		&session.ProviderSessionID,
		&startedAt,
		&updatedAt,
		&endedAt,
	); err != nil {
		return execution.Session{}, err
	}
	session.Role = worker.Role(role)
	session.Status = execution.SessionStatus(status)
	var err error
	session.StartedAt, err = parseExecutionTime(startedAt)
	if err != nil {
		return execution.Session{}, fmt.Errorf("parse session %q start time: %w", session.ID, err)
	}
	session.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return execution.Session{}, fmt.Errorf("parse session %q update time: %w", session.ID, err)
	}
	session.EndedAt, err = parseOptionalExecutionTime(endedAt)
	if err != nil {
		return execution.Session{}, fmt.Errorf("parse session %q end time: %w", session.ID, err)
	}
	if err := session.Validate(); err != nil {
		return execution.Session{}, err
	}
	return session, nil
}

func findExecutionEvent(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (execution.Event, bool, error) {
	event, err := scanExecutionEvent(tx.QueryRowContext(
		ctx,
		`SELECT id, session_id, sequence, event_type, text, occurred_at
		 FROM session_events WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Event{}, false, nil
	}
	if err != nil {
		return execution.Event{}, false, fmt.Errorf("select session event %q: %w", id, err)
	}
	return event, true, nil
}

func scanExecutionEvent(scanner executionScanner) (execution.Event, error) {
	event := execution.Event{}
	var eventType string
	var occurredAt string
	if err := scanner.Scan(
		&event.ID,
		&event.SessionID,
		&event.Sequence,
		&eventType,
		&event.Text,
		&occurredAt,
	); err != nil {
		return execution.Event{}, err
	}
	event.Type = worker.EventType(eventType)
	var err error
	event.OccurredAt, err = parseExecutionTime(occurredAt)
	if err != nil {
		return execution.Event{}, fmt.Errorf("parse session event %q time: %w", event.ID, err)
	}
	if err := event.Validate(); err != nil {
		return execution.Event{}, err
	}
	return event, nil
}

func scanExecutionCommand(scanner executionScanner) (execution.Command, error) {
	command := execution.Command{}
	var commandType string
	var status string
	var requestedAt string
	var appliedAt sql.NullString
	if err := scanner.Scan(
		&command.ID,
		&command.SessionID,
		&commandType,
		&command.Message,
		&status,
		&requestedAt,
		&appliedAt,
		&command.Error,
	); err != nil {
		return execution.Command{}, err
	}
	command.Type = worker.CommandType(commandType)
	command.Status = execution.CommandStatus(status)
	var err error
	command.RequestedAt, err = parseExecutionTime(requestedAt)
	if err != nil {
		return execution.Command{}, fmt.Errorf("parse session command %q request time: %w", command.ID, err)
	}
	command.AppliedAt, err = parseOptionalExecutionTime(appliedAt)
	if err != nil {
		return execution.Command{}, fmt.Errorf("parse session command %q applied time: %w", command.ID, err)
	}
	if err := command.Validate(); err != nil {
		return execution.Command{}, err
	}
	return command, nil
}

func requireExecutionInsert(result sql.Result, kind string, id string) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read inserted %s %q row count: %w", kind, id, err)
	}
	if rowsAffected == 0 {
		return execution.ErrAlreadyExists
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"insert %s %q: expected one affected row, got %d",
			kind,
			id,
			rowsAffected,
		)
	}
	return nil
}

func formatExecutionTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalExecutionTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatExecutionTime(*value)
}

func parseExecutionTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseOptionalExecutionTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseExecutionTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
