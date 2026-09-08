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

func (s *ExecutionStore) TransitionRun(
	ctx context.Context,
	transition execution.RunTransition,
) (execution.Run, error) {
	if err := transition.Validate(); err != nil {
		return execution.Run{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, fmt.Errorf("begin run transition: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason, started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		transition.RunID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Run{}, fmt.Errorf("select run %q for transition: %w", transition.RunID, err)
	}
	if run.Status != transition.Expected {
		return execution.Run{}, execution.ErrStateConflict
	}

	run.Status = transition.Status
	run.Reason = transition.Reason
	run.UpdatedAt = transition.OccurredAt.UTC()
	run.EndedAt = nil
	if transition.Status.IsTerminal() {
		endedAt := run.UpdatedAt
		run.EndedAt = &endedAt
	}
	if err := run.Validate(); err != nil {
		return execution.Run{}, err
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE runs
		 SET status = ?, reason = ?, updated_at = ?, ended_at = ?
		 WHERE id = ? AND status = ?`,
		run.Status,
		run.Reason,
		formatExecutionTime(run.UpdatedAt),
		formatOptionalExecutionTime(run.EndedAt),
		run.ID,
		transition.Expected,
	)
	if err != nil {
		return execution.Run{}, fmt.Errorf("update run %q: %w", run.ID, err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return execution.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, fmt.Errorf("commit run %q transition: %w", run.ID, err)
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

func (s *ExecutionStore) TransitionSession(
	ctx context.Context,
	transition execution.SessionTransition,
) (execution.Session, error) {
	if err := transition.Validate(); err != nil {
		return execution.Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Session{}, fmt.Errorf("begin session transition: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	session, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		transition.SessionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Session{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Session{}, fmt.Errorf(
			"select session %q for transition: %w",
			transition.SessionID,
			err,
		)
	}
	if session.Status != transition.Expected {
		return execution.Session{}, execution.ErrStateConflict
	}
	if transition.ProviderSessionID != "" {
		if session.ProviderSessionID != "" &&
			session.ProviderSessionID != transition.ProviderSessionID {
			return execution.Session{}, execution.ErrStateConflict
		}
		session.ProviderSessionID = transition.ProviderSessionID
	}

	session.Status = transition.Status
	session.UpdatedAt = transition.OccurredAt.UTC()
	session.EndedAt = nil
	if transition.Status.IsTerminal() {
		endedAt := session.UpdatedAt
		session.EndedAt = &endedAt
	}
	if err := session.Validate(); err != nil {
		return execution.Session{}, err
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE sessions
		 SET status = ?, provider_session_id = ?, updated_at = ?, ended_at = ?
		 WHERE id = ? AND status = ?`,
		session.Status,
		session.ProviderSessionID,
		formatExecutionTime(session.UpdatedAt),
		formatOptionalExecutionTime(session.EndedAt),
		session.ID,
		transition.Expected,
	)
	if err != nil {
		return execution.Session{}, fmt.Errorf("update session %q: %w", session.ID, err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return execution.Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Session{}, fmt.Errorf("commit session %q transition: %w", session.ID, err)
	}
	return session, nil
}

func (s *ExecutionStore) AppendEvent(
	ctx context.Context,
	pending execution.PendingEvent,
) (execution.Event, bool, error) {
	if err := pending.Validate(); err != nil {
		return execution.Event{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Event{}, false, fmt.Errorf("begin session event: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	existing, found, err := findExecutionEvent(ctx, tx, pending.ID)
	if err != nil {
		return execution.Event{}, false, err
	}
	if found {
		if existing.SessionID != pending.SessionID ||
			existing.Type != pending.Type ||
			existing.Text != pending.Text {
			return execution.Event{}, false, execution.ErrEventConflict
		}
		return existing, false, nil
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
		ID:         pending.ID,
		SessionID:  pending.SessionID,
		Sequence:   sequence,
		Type:       pending.Type,
		Text:       pending.Text,
		OccurredAt: pending.OccurredAt.UTC(),
	}
	if err := event.Validate(); err != nil {
		return execution.Event{}, false, err
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
		return execution.Event{}, false, fmt.Errorf("insert session event %q: %w", event.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Event{}, false, fmt.Errorf("commit session event %q: %w", event.ID, err)
	}
	return event, true, nil
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
) (execution.Command, bool, error) {
	if err := command.Validate(); err != nil {
		return execution.Command{}, false, err
	}
	if command.Status != execution.CommandStatusPending {
		return execution.Command{}, false, fmt.Errorf(
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
		return execution.Command{}, false, fmt.Errorf("insert session command %q: %w", command.ID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return execution.Command{}, false, fmt.Errorf("read inserted command %q row count: %w", command.ID, err)
	}
	if rowsAffected == 1 {
		return command, true, nil
	}
	if rowsAffected != 0 {
		return execution.Command{}, false, fmt.Errorf(
			"insert session command %q: expected zero or one affected row, got %d",
			command.ID,
			rowsAffected,
		)
	}
	existing, err := s.GetCommand(ctx, command.ID)
	if err != nil {
		return execution.Command{}, false, err
	}
	if existing.SessionID != command.SessionID ||
		existing.Type != command.Type ||
		existing.Message != command.Message {
		return execution.Command{}, false, execution.ErrCommandConflict
	}
	return existing, false, nil
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

func (s *ExecutionStore) ResolveCommand(
	ctx context.Context,
	resolution execution.CommandResolution,
) (execution.Command, error) {
	if err := resolution.Validate(); err != nil {
		return execution.Command{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Command{}, fmt.Errorf("begin command resolution: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	command, err := scanExecutionCommand(tx.QueryRowContext(
		ctx,
		`SELECT id, session_id, command_type, message, status,
		        requested_at, applied_at, error
		 FROM session_commands WHERE id = ?`,
		resolution.CommandID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Command{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Command{}, fmt.Errorf(
			"select session command %q for resolution: %w",
			resolution.CommandID,
			err,
		)
	}
	if command.Status != execution.CommandStatusPending {
		if command.Status == resolution.Status && command.Error == resolution.Error {
			return command, nil
		}
		return execution.Command{}, execution.ErrCommandConflict
	}

	appliedAt := resolution.AppliedAt.UTC()
	command.Status = resolution.Status
	command.AppliedAt = &appliedAt
	command.Error = resolution.Error
	if err := command.Validate(); err != nil {
		return execution.Command{}, err
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE session_commands
		 SET status = ?, applied_at = ?, error = ?
		 WHERE id = ? AND status = ?`,
		command.Status,
		formatExecutionTime(appliedAt),
		command.Error,
		command.ID,
		execution.CommandStatusPending,
	)
	if err != nil {
		return execution.Command{}, fmt.Errorf("resolve session command %q: %w", command.ID, err)
	}
	if err := requireExecutionUpdate(result, "session command", command.ID); err != nil {
		return execution.Command{}, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Command{}, fmt.Errorf("commit session command %q resolution: %w", command.ID, err)
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

func requireExecutionUpdate(result sql.Result, kind string, id string) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated %s %q row count: %w", kind, id, err)
	}
	if rowsAffected == 0 {
		return execution.ErrStateConflict
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"update %s %q: expected one affected row, got %d",
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
