package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// ExtendPlanningRoundLimit records one user-approved extra planning round.
// The action ID makes a retried click a no-op even if the extra round already
// completed and the run has reached its new cap.
func (s *ExecutionStore) ExtendPlanningRoundLimit(
	ctx context.Context,
	extension execution.PlanningRoundExtension,
) (execution.Run, bool, error) {
	if err := extension.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("begin planning-round extension: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var storedRunID string
	err = tx.QueryRowContext(
		ctx, `SELECT run_id FROM planning_round_extensions WHERE id = ?`, extension.ID,
	).Scan(&storedRunID)
	if err == nil {
		if storedRunID != extension.RunID {
			return execution.Run{}, false, execution.ErrRunActionConflict
		}
		run, getErr := scanRunForPause(ctx, tx, extension.RunID)
		return run, false, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, false, fmt.Errorf("find planning-round extension: %w", err)
	}

	run, err := scanRunForPause(ctx, tx, extension.RunID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		run.WaitKind != execution.RunWaitKindRoundCap ||
		run.PlanningRoundLimit != extension.ExpectedLimit {
		return execution.Run{}, false, execution.ErrStateConflict
	}

	run.PlanningRoundLimit++
	run.UpdatedAt = extension.OccurredAt.UTC()
	if err := run.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE runs SET planning_round_limit = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND paused = 0 AND wait_kind = ? AND planning_round_limit = ?`,
		run.PlanningRoundLimit,
		formatExecutionTime(run.UpdatedAt),
		run.ID,
		execution.RunStatusWaitingForUser,
		execution.RunWaitKindRoundCap,
		extension.ExpectedLimit,
	)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("extend planning-round limit: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return execution.Run{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO planning_round_extensions
		 (id, run_id, previous_limit, extended_limit, occurred_at)
		 VALUES (?, ?, ?, ?, ?)`,
		extension.ID,
		run.ID,
		extension.ExpectedLimit,
		run.PlanningRoundLimit,
		formatExecutionTime(run.UpdatedAt),
	); err != nil {
		return execution.Run{}, false, fmt.Errorf("record planning-round extension: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, false, fmt.Errorf("commit planning-round extension: %w", err)
	}
	return run, true, nil
}
