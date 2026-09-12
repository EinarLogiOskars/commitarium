package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func (s *ExecutionStore) ApplyRunPause(
	ctx context.Context,
	mutation execution.RunPauseMutation,
) (execution.Run, bool, error) {
	if err := mutation.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("begin run pause action: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var storedRunID string
	var storedAction execution.RunPauseAction
	err = tx.QueryRowContext(
		ctx, `SELECT run_id, action FROM run_actions WHERE id = ?`, mutation.ID,
	).Scan(&storedRunID, &storedAction)
	if err == nil {
		if storedRunID != mutation.RunID || storedAction != mutation.Action {
			return execution.Run{}, false, execution.ErrRunActionConflict
		}
		run, getErr := scanRunForPause(ctx, tx, mutation.RunID)
		return run, false, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, false, fmt.Errorf("find run pause action: %w", err)
	}

	run, err := scanRunForPause(ctx, tx, mutation.RunID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status.IsTerminal() {
		return execution.Run{}, false, execution.ErrInvalidStatusTransition
	}

	switch mutation.Action {
	case execution.RunPauseActionPause:
		if !run.Paused {
			run.Paused = true
			if run.Status == execution.RunStatusWaitingForUser {
				run.PausedFromWaitKind = run.WaitKind
			}
			run.WaitKind = execution.RunWaitKindPaused
		}
	case execution.RunPauseActionResume:
		if run.Paused {
			run.Paused = false
			if run.Status == execution.RunStatusWaitingForUser {
				run.WaitKind = run.PausedFromWaitKind
				if run.WaitKind == "" {
					run.WaitKind = execution.RunWaitKindBlocker
				}
			} else {
				run.WaitKind = ""
			}
			run.PausedFromWaitKind = ""
		}
	}
	run.UpdatedAt = mutation.OccurredAt.UTC()
	if err := run.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE runs SET wait_kind = ?, paused = ?, paused_from_wait_kind = ?, updated_at = ? WHERE id = ?`,
		run.WaitKind, run.Paused, run.PausedFromWaitKind,
		formatExecutionTime(run.UpdatedAt), run.ID,
	); err != nil {
		return execution.Run{}, false, fmt.Errorf("update run pause state: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO run_actions (id, run_id, action, occurred_at) VALUES (?, ?, ?, ?)`,
		mutation.ID, mutation.RunID, mutation.Action, formatExecutionTime(mutation.OccurredAt),
	); err != nil {
		return execution.Run{}, false, fmt.Errorf("record run pause action: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, false, fmt.Errorf("commit run pause action: %w", err)
	}
	return run, true, nil
}

func scanRunForPause(ctx context.Context, tx *sql.Tx, runID string) (execution.Run, error) {
	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason, wait_kind, paused, paused_from_wait_kind,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider, merge_policy, autonomy_policy, plan_version,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		runID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Run{}, fmt.Errorf("select run %q for pause action: %w", runID, err)
	}
	return run, nil
}
