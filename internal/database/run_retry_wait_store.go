package database

import (
	"context"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// RecordRetryWait records, or replaces, a run's retry of an interrupted turn.
func (s *ExecutionStore) RecordRetryWait(ctx context.Context, wait execution.RetryWait) error {
	dueAt := ""
	if wait.DueAt != nil {
		dueAt = formatExecutionTime(*wait.DueAt)
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO run_retry_waits (run_id, session_id, attempt_id, retries, cause, due_at, turn, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (run_id) DO UPDATE SET session_id = excluded.session_id, attempt_id = excluded.attempt_id,
		                                    retries = excluded.retries, cause = excluded.cause,
		                                    due_at = excluded.due_at, turn = excluded.turn,
		                                    created_at = excluded.created_at`,
		wait.RunID, wait.SessionID, wait.AttemptID, wait.Retries, wait.Cause, dueAt, wait.Turn,
		formatExecutionTime(wait.CreatedAt),
	); err != nil {
		return fmt.Errorf("record retry wait for run %q: %w", wait.RunID, err)
	}
	return nil
}

// ClearRetryWait forgets a run's retry wait, if any.
func (s *ExecutionStore) ClearRetryWait(ctx context.Context, runID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM run_retry_waits WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("clear retry wait for run %q: %w", runID, err)
	}
	return nil
}

// RetryWaitForRun returns a run's retry wait, or an empty one.
func (s *ExecutionStore) RetryWaitForRun(ctx context.Context, runID string) (execution.RetryWait, error) {
	rows, err := s.queryRetryWaits(ctx, `WHERE run_id = ?`, runID)
	if err != nil || len(rows) == 0 {
		return execution.RetryWait{}, err
	}
	return rows[0], nil
}

// RetryWaits lists every run's retry wait, oldest first.
func (s *ExecutionStore) RetryWaits(ctx context.Context) ([]execution.RetryWait, error) {
	return s.queryRetryWaits(ctx, ``)
}

func (s *ExecutionStore) queryRetryWaits(ctx context.Context, where string, args ...any) ([]execution.RetryWait, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, session_id, attempt_id, retries, cause, due_at, turn, created_at
		 FROM run_retry_waits `+where+` ORDER BY created_at, run_id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("list retry waits: %w", err)
	}
	defer rows.Close()
	waits := make([]execution.RetryWait, 0)
	for rows.Next() {
		var wait execution.RetryWait
		var dueAt, created string
		if err := rows.Scan(
			&wait.RunID, &wait.SessionID, &wait.AttemptID, &wait.Retries, &wait.Cause, &dueAt, &wait.Turn, &created,
		); err != nil {
			return nil, fmt.Errorf("scan retry wait: %w", err)
		}
		if dueAt != "" {
			parsed, err := parseExecutionTime(dueAt)
			if err != nil {
				return nil, err
			}
			wait.DueAt = &parsed
		}
		if wait.CreatedAt, err = parseExecutionTime(created); err != nil {
			return nil, err
		}
		waits = append(waits, wait)
	}
	return waits, rows.Err()
}
