package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// ResolveInterventionGuidance consumes an answered guidance result and releases
// the pause gate atomically. This prevents a crash from unpausing the workflow
// while leaving the intervention looking unresolved, or vice versa.
func (s *ExecutionStore) ResolveInterventionGuidance(
	ctx context.Context,
	resolution execution.InterventionGuidanceResolution,
) (execution.Run, bool, error) {
	if err := resolution.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("begin intervention resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var storedRunID string
	var storedAction execution.RunPauseAction
	err = tx.QueryRowContext(
		ctx, `SELECT run_id, action FROM run_actions WHERE id = ?`, resolution.ID,
	).Scan(&storedRunID, &storedAction)
	if err == nil {
		if storedRunID != resolution.RunID || storedAction != execution.RunPauseActionResume {
			return execution.Run{}, false, execution.ErrRunActionConflict
		}
		intervention, found, findErr := findIntervention(ctx, tx, resolution.InterventionID)
		if findErr != nil {
			return execution.Run{}, false, findErr
		}
		if !found || intervention.RunID != resolution.RunID || intervention.ResolvedAt == nil ||
			intervention.ResolutionActionID != resolution.ID {
			return execution.Run{}, false, execution.ErrRunActionConflict
		}
		run, getErr := scanRunForPause(ctx, tx, resolution.RunID)
		return run, false, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, false, fmt.Errorf("find intervention resolution action: %w", err)
	}

	intervention, found, err := findIntervention(ctx, tx, resolution.InterventionID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !found {
		return execution.Run{}, false, execution.ErrNotFound
	}
	if intervention.RunID != resolution.RunID ||
		intervention.Status != execution.InterventionStatusAnswered ||
		intervention.Effect != worker.InterventionEffectGuidanceApplied ||
		intervention.ResolvedAt != nil {
		return execution.Run{}, false, execution.ErrStateConflict
	}
	var latestID string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT id FROM run_interventions
		 WHERE run_id = ? ORDER BY requested_at DESC, id DESC LIMIT 1`,
		resolution.RunID,
	).Scan(&latestID); err != nil {
		return execution.Run{}, false, fmt.Errorf("select latest intervention for resolution: %w", err)
	}
	if latestID != intervention.ID {
		return execution.Run{}, false, execution.ErrStateConflict
	}

	run, err := scanRunForPause(ctx, tx, resolution.RunID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status != execution.RunStatusWaitingForUser || !run.Paused ||
		run.WaitKind != execution.RunWaitKindPaused {
		return execution.Run{}, false, execution.ErrStateConflict
	}
	run.Paused = false
	run.WaitKind = run.PausedFromWaitKind
	if run.WaitKind == "" {
		run.WaitKind = execution.RunWaitKindBlocker
	}
	run.PausedFromWaitKind = ""
	if strings.TrimSpace(intervention.ResumeReason) != "" {
		run.Reason = intervention.ResumeReason
	} else {
		run.Reason = "The workflow is ready to continue after the user's intervention."
	}
	run.UpdatedAt = resolution.OccurredAt.UTC()
	if err := run.Validate(); err != nil {
		return execution.Run{}, false, err
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE run_interventions
		 SET resolved_at = ?, resolution_action_id = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND effect = ? AND resolved_at IS NULL`,
		formatExecutionTime(run.UpdatedAt), resolution.ID, formatExecutionTime(run.UpdatedAt),
		intervention.ID, execution.InterventionStatusAnswered,
		worker.InterventionEffectGuidanceApplied,
	)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("resolve intervention %q: %w", intervention.ID, err)
	}
	if err := requireExecutionUpdate(result, "intervention", intervention.ID); err != nil {
		return execution.Run{}, false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE runs SET reason = ?, wait_kind = ?, paused = 0,
		        paused_from_wait_kind = '', updated_at = ?
		 WHERE id = ? AND status = ? AND paused = 1 AND wait_kind = ?`,
		run.Reason, run.WaitKind, formatExecutionTime(run.UpdatedAt), run.ID,
		execution.RunStatusWaitingForUser, execution.RunWaitKindPaused,
	)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("release intervention pause: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return execution.Run{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO run_actions (id, run_id, action, occurred_at) VALUES (?, ?, ?, ?)`,
		resolution.ID, resolution.RunID, execution.RunPauseActionResume,
		formatExecutionTime(run.UpdatedAt),
	); err != nil {
		return execution.Run{}, false, fmt.Errorf("record intervention resolution action: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, false, fmt.Errorf("commit intervention resolution: %w", err)
	}
	return run, true, nil
}
