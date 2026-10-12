package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// RecordLimitWait notes that a run waits for an agent's usage limit to reset.
func (s *ExecutionStore) RecordLimitWait(ctx context.Context, wait execution.LimitWait) error {
	resetsAt := ""
	if wait.ResetsAt != nil {
		resetsAt = formatExecutionTime(*wait.ResetsAt)
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO run_limit_waits (run_id, agent_id, role, resets_at, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (run_id) DO UPDATE SET agent_id = excluded.agent_id, role = excluded.role,
		                                    resets_at = excluded.resets_at, created_at = excluded.created_at`,
		wait.RunID, wait.AgentID, string(wait.Role), resetsAt, formatExecutionTime(wait.CreatedAt),
	); err != nil {
		return fmt.Errorf("record limit wait for run %q: %w", wait.RunID, err)
	}
	return nil
}

// ClearLimitWait forgets a run's limit wait, if any.
func (s *ExecutionStore) ClearLimitWait(ctx context.Context, runID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM run_limit_waits WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("clear limit wait for run %q: %w", runID, err)
	}
	return nil
}

// LimitWaitForRun returns the limit a run waits on, or an empty wait.
func (s *ExecutionStore) LimitWaitForRun(ctx context.Context, runID string) (execution.LimitWait, error) {
	rows, err := s.queryLimitWaits(ctx, `WHERE run_id = ?`, runID)
	if err != nil || len(rows) == 0 {
		return execution.LimitWait{}, err
	}
	return rows[0], nil
}

// LimitWaits lists every run waiting on a usage limit, oldest first.
func (s *ExecutionStore) LimitWaits(ctx context.Context) ([]execution.LimitWait, error) {
	return s.queryLimitWaits(ctx, ``)
}

func (s *ExecutionStore) queryLimitWaits(ctx context.Context, where string, args ...any) ([]execution.LimitWait, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, agent_id, role, resets_at, created_at FROM run_limit_waits `+where+` ORDER BY created_at, run_id`,
		args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list limit waits: %w", err)
	}
	defer rows.Close()
	waits := make([]execution.LimitWait, 0)
	for rows.Next() {
		var wait execution.LimitWait
		var role, resetsAt, created string
		if err := rows.Scan(&wait.RunID, &wait.AgentID, &role, &resetsAt, &created); err != nil {
			return nil, fmt.Errorf("scan limit wait: %w", err)
		}
		wait.Role = worker.Role(role)
		if resetsAt != "" {
			parsed, err := parseExecutionTime(resetsAt)
			if err != nil {
				return nil, err
			}
			wait.ResetsAt = &parsed
		}
		if wait.CreatedAt, err = parseExecutionTime(created); err != nil {
			return nil, err
		}
		waits = append(waits, wait)
	}
	return waits, rows.Err()
}
