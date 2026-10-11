package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

// ReopenFailedRun turns a failed run back into a blocker pause at its last
// turn (ADR-017). The most recently failed session becomes active again so
// that continuing retries its turn; other sessions stay as they are. A run
// already reopened is returned unchanged.
func (s *ExecutionStore) ReopenFailedRun(
	ctx context.Context,
	runID string,
	reason string,
	occurredAt time.Time,
) (execution.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, fmt.Errorf("begin run reopen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status, waitKind, featureState string
	err = tx.QueryRowContext(
		ctx,
		`SELECT r.status, r.wait_kind, f.state
		   FROM runs r JOIN features f ON f.id = r.feature_id
		  WHERE r.id = ?`,
		runID,
	).Scan(&status, &waitKind, &featureState)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Run{}, fmt.Errorf("select run %q for reopen: %w", runID, err)
	}
	if execution.RunStatus(status) == execution.RunStatusWaitingForUser &&
		execution.RunWaitKind(waitKind) == execution.RunWaitKindBlocker {
		return s.GetRun(ctx, runID)
	}
	if execution.RunStatus(status) != execution.RunStatusFailed || feature.State(featureState).IsTerminal() {
		return execution.Run{}, execution.ErrStateConflict
	}
	at := formatExecutionTime(occurredAt)
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE sessions
		    SET status = ?, updated_at = ?, ended_at = NULL
		  WHERE id = (SELECT id FROM sessions
		               WHERE run_id = ? AND status = ?
		               ORDER BY updated_at DESC, id DESC LIMIT 1)`,
		execution.SessionStatusPauseRequested, at, runID, execution.SessionStatusFailed,
	); err != nil {
		return execution.Run{}, fmt.Errorf("reopen failed session of run %q: %w", runID, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE runs
		    SET status = ?, reason = ?, wait_kind = ?, paused = 0, paused_from_wait_kind = '',
		        updated_at = ?, ended_at = NULL
		  WHERE id = ? AND status = ?`,
		execution.RunStatusWaitingForUser, reason, execution.RunWaitKindBlocker, at,
		runID, execution.RunStatusFailed,
	); err != nil {
		return execution.Run{}, fmt.Errorf("reopen run %q: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, fmt.Errorf("commit run reopen: %w", err)
	}
	return s.GetRun(ctx, runID)
}
