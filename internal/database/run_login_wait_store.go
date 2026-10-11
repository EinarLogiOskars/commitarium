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

// RecordLoginWait notes that a run waits for an agent to log in again.
func (s *ExecutionStore) RecordLoginWait(
	ctx context.Context,
	runID string,
	agentID string,
	role worker.Role,
	occurredAt time.Time,
) error {
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO run_login_waits (run_id, agent_id, role, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (run_id) DO UPDATE SET agent_id = excluded.agent_id, role = excluded.role,
		                                    created_at = excluded.created_at`,
		runID, agentID, string(role), formatExecutionTime(occurredAt),
	); err != nil {
		return fmt.Errorf("record login wait for run %q: %w", runID, err)
	}
	return nil
}

// ClearLoginWait forgets a run's login wait, if any.
func (s *ExecutionStore) ClearLoginWait(ctx context.Context, runID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM run_login_waits WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("clear login wait for run %q: %w", runID, err)
	}
	return nil
}

// LoginWaitForRun returns the agent a run waits on, or an empty wait.
func (s *ExecutionStore) LoginWaitForRun(ctx context.Context, runID string) (execution.LoginWait, error) {
	var wait execution.LoginWait
	var role string
	err := s.db.QueryRowContext(
		ctx, `SELECT agent_id, role FROM run_login_waits WHERE run_id = ?`, runID,
	).Scan(&wait.AgentID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.LoginWait{}, nil
	}
	if err != nil {
		return execution.LoginWait{}, fmt.Errorf("load login wait for run %q: %w", runID, err)
	}
	wait.Role = worker.Role(role)
	return wait, nil
}

// RunsAwaitingLogin lists the runs waiting for an agent to log in again.
func (s *ExecutionStore) RunsAwaitingLogin(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.db.QueryContext(
		ctx, `SELECT run_id FROM run_login_waits WHERE agent_id = ? ORDER BY created_at, run_id`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list runs awaiting login of agent %q: %w", agentID, err)
	}
	defer rows.Close()
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan run awaiting login: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	return runIDs, rows.Err()
}
