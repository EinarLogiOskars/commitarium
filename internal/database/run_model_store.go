package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// ChangeRunModels sets a run's models, records the changes, and marks the
// attempts stopped to switch, in one transaction.
func (s *ExecutionStore) ChangeRunModels(
	ctx context.Context,
	runID string,
	models project.AgentModels,
	changes []execution.ModelChange,
	stopAttempts []string,
	occurredAt time.Time,
) (execution.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, fmt.Errorf("begin model change for run %q: %w", runID, err)
	}
	defer func() { _ = tx.Rollback() }()
	at := formatExecutionTime(occurredAt)
	result, err := tx.ExecContext(ctx,
		`UPDATE runs SET lead_model = ?, reviewer_model = ?, updated_at = ?
		 WHERE id = ? AND status NOT IN ('succeeded', 'failed', 'stopped')`,
		models.Lead, models.Reviewer, at, runID)
	if err != nil {
		return execution.Run{}, fmt.Errorf("change models of run %q: %w", runID, err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return execution.Run{}, fmt.Errorf("%w: run %q has ended or does not exist", execution.ErrInvalidRun, runID)
	}
	for _, change := range changes {
		stopped := 0
		if change.Stopped {
			stopped = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO run_model_changes (run_id, role, from_model, to_model, stopped, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			runID, string(change.Role), change.FromModel, change.ToModel, stopped, at); err != nil {
			return execution.Run{}, fmt.Errorf("record model change for run %q: %w", runID, err)
		}
	}
	for _, attemptID := range stopAttempts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO run_model_switches (run_id, attempt_id, created_at) VALUES (?, ?, ?)
			 ON CONFLICT (run_id, attempt_id) DO NOTHING`, runID, attemptID, at); err != nil {
			return execution.Run{}, fmt.Errorf("mark attempt %q for a model switch: %w", attemptID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, fmt.Errorf("commit model change for run %q: %w", runID, err)
	}
	return s.GetRun(ctx, runID)
}

// TakeModelSwitch removes an attempt's switch mark and reports whether it had
// one.
func (s *ExecutionStore) TakeModelSwitch(ctx context.Context, runID, attemptID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM run_model_switches WHERE run_id = ? AND attempt_id = ?`, runID, attemptID)
	if err != nil {
		return false, fmt.Errorf("take model switch of attempt %q: %w", attemptID, err)
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// HasModelSwitch reports whether an attempt carries a switch mark.
func (s *ExecutionStore) HasModelSwitch(ctx context.Context, runID, attemptID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_model_switches WHERE run_id = ? AND attempt_id = ?`, runID, attemptID,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("read model switch of attempt %q: %w", attemptID, err)
	}
	return count > 0, nil
}

// ModelChangesForRun lists a run's model changes, oldest first.
func (s *ExecutionStore) ModelChangesForRun(ctx context.Context, runID string) ([]execution.ModelChange, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role, from_model, to_model, stopped, created_at FROM run_model_changes WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list model changes of run %q: %w", runID, err)
	}
	defer rows.Close()
	changes := make([]execution.ModelChange, 0)
	for rows.Next() {
		var change execution.ModelChange
		var role, created string
		var stopped int
		if err := rows.Scan(&role, &change.FromModel, &change.ToModel, &stopped, &created); err != nil {
			return nil, fmt.Errorf("scan model change of run %q: %w", runID, err)
		}
		change.Role, change.Stopped = worker.Role(role), stopped == 1
		if change.CreatedAt, err = parseExecutionTime(created); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return changes, nil
}
