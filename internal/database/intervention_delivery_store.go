package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// BeginInterventionTurn atomically binds a queued user message to one resume
// attempt. The run deliberately stays paused and waiting; only the selected
// conversation becomes active while it answers the intervention.
func (s *ExecutionStore) BeginInterventionTurn(
	ctx context.Context,
	admission execution.InterventionTurnAdmission,
) (bool, error) {
	if err := admission.Validate(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin intervention turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	intervention, found, err := findIntervention(ctx, tx, admission.InterventionID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, execution.ErrNotFound
	}
	if (intervention.Status == execution.InterventionStatusBeingAnswered ||
		intervention.Status == execution.InterventionStatusAnswered) &&
		intervention.AttemptID == admission.NextAttempt.AttemptID {
		return false, nil
	}
	if intervention.Status != execution.InterventionStatusQueued {
		return false, execution.ErrStateConflict
	}

	session, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		intervention.SessionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return false, execution.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("select intervention session: %w", err)
	}
	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason, wait_kind, paused, paused_from_wait_kind,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider, merge_policy, autonomy_policy, plan_version,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		intervention.RunID,
	))
	if err != nil {
		return false, fmt.Errorf("select intervention run: %w", err)
	}
	if session.RunID != run.ID || session.Role != intervention.Target ||
		session.Status != execution.SessionStatusWaitingForUser ||
		session.ProviderSessionID == "" || !run.Paused ||
		run.Status != execution.RunStatusWaitingForUser ||
		run.WaitKind != execution.RunWaitKindPaused {
		return false, execution.ErrStateConflict
	}
	var activeCount int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sessions
		 WHERE run_id = ? AND status NOT IN (?, ?, ?, ?)`,
		run.ID,
		execution.SessionStatusWaitingForUser,
		execution.SessionStatusCompleted,
		execution.SessionStatusStopped,
		execution.SessionStatusFailed,
	).Scan(&activeCount); err != nil {
		return false, fmt.Errorf("count active sessions for intervention: %w", err)
	}
	if activeCount != 0 {
		return false, execution.ErrStateConflict
	}
	checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		session.ID,
	))
	if err != nil {
		return false, fmt.Errorf("select intervention worker checkpoint: %w", err)
	}
	if checkpoint.AttemptID != admission.PreviousAttemptID ||
		checkpoint.LastEventSequence != admission.PreviousLastEventSequence {
		return false, execution.ErrWorkerAttemptConflict
	}

	next := admission.NextAttempt
	result, err := tx.ExecContext(
		ctx,
		`UPDATE worker_attempt_checkpoints
		 SET attempt_id = ?, last_event_sequence = 0, created_at = ?, updated_at = ?
		 WHERE session_id = ? AND attempt_id = ? AND last_event_sequence = ?`,
		next.AttemptID, formatExecutionTime(next.CreatedAt), formatExecutionTime(next.UpdatedAt),
		next.SessionID, admission.PreviousAttemptID, admission.PreviousLastEventSequence,
	)
	if err != nil {
		return false, fmt.Errorf("replace intervention worker attempt: %w", err)
	}
	if err := requireExecutionUpdate(result, "worker attempt", admission.PreviousAttemptID); err != nil {
		return false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		execution.SessionStatusRunning, formatExecutionTime(admission.OccurredAt),
		session.ID, execution.SessionStatusWaitingForUser,
	)
	if err != nil {
		return false, fmt.Errorf("activate intervention session: %w", err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE run_interventions
		 SET status = ?, attempt_id = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND attempt_id = ''`,
		execution.InterventionStatusBeingAnswered, next.AttemptID,
		formatExecutionTime(admission.OccurredAt), intervention.ID,
		execution.InterventionStatusQueued,
	)
	if err != nil {
		return false, fmt.Errorf("activate intervention: %w", err)
	}
	if err := requireExecutionUpdate(result, "intervention", intervention.ID); err != nil {
		return false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE runs SET reason = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND paused = 1 AND wait_kind = ?`,
		admission.RunReason, formatExecutionTime(admission.OccurredAt), run.ID,
		execution.RunStatusWaitingForUser, execution.RunWaitKindPaused,
	)
	if err != nil {
		return false, fmt.Errorf("update intervention run: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit intervention turn: %w", err)
	}
	return true, nil
}

// CompleteIntervention records the structured effect and returns the selected
// conversation to rest in the same transaction. It never clears the pause or
// advances the saved workflow checkpoint.
func (s *ExecutionStore) CompleteIntervention(
	ctx context.Context,
	completion execution.InterventionCompletion,
) (execution.Intervention, bool, error) {
	if err := completion.Validate(); err != nil {
		return execution.Intervention{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("begin intervention completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	intervention, found, err := findIntervention(ctx, tx, completion.InterventionID)
	if err != nil {
		return execution.Intervention{}, false, err
	}
	if !found {
		return execution.Intervention{}, false, execution.ErrNotFound
	}
	if intervention.Status == execution.InterventionStatusAnswered {
		if intervention.AttemptID == completion.AttemptID && intervention.Effect == completion.Effect {
			return intervention, false, nil
		}
		return execution.Intervention{}, false, execution.ErrInterventionConflict
	}
	if intervention.Status != execution.InterventionStatusBeingAnswered ||
		intervention.AttemptID != completion.AttemptID {
		return execution.Intervention{}, false, execution.ErrStateConflict
	}
	session, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		intervention.SessionID,
	))
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("select completed intervention session: %w", err)
	}
	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason, wait_kind, paused, paused_from_wait_kind,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider, merge_policy, autonomy_policy, plan_version,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		intervention.RunID,
	))
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("select completed intervention run: %w", err)
	}
	checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		session.ID,
	))
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("select completed intervention checkpoint: %w", err)
	}
	if session.RunID != run.ID || session.Role != intervention.Target ||
		session.Status != execution.SessionStatusRunning ||
		session.ProviderSessionID != completion.ProviderSessionID ||
		checkpoint.AttemptID != completion.AttemptID || !run.Paused ||
		run.Status != execution.RunStatusWaitingForUser ||
		run.WaitKind != execution.RunWaitKindPaused {
		return execution.Intervention{}, false, execution.ErrStateConflict
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		execution.SessionStatusWaitingForUser, formatExecutionTime(completion.OccurredAt),
		session.ID, execution.SessionStatusRunning,
	)
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("finish intervention session: %w", err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return execution.Intervention{}, false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE run_interventions
		 SET status = ?, effect = ?, updated_at = ?, answered_at = ?
		 WHERE id = ? AND status = ? AND attempt_id = ?`,
		execution.InterventionStatusAnswered, completion.Effect,
		formatExecutionTime(completion.OccurredAt), formatExecutionTime(completion.OccurredAt),
		intervention.ID, execution.InterventionStatusBeingAnswered, completion.AttemptID,
	)
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("finish intervention: %w", err)
	}
	if err := requireExecutionUpdate(result, "intervention", intervention.ID); err != nil {
		return execution.Intervention{}, false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE runs SET reason = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND paused = 1 AND wait_kind = ?`,
		completion.RunReason, formatExecutionTime(completion.OccurredAt), run.ID,
		execution.RunStatusWaitingForUser, execution.RunWaitKindPaused,
	)
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("finish intervention run: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return execution.Intervention{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Intervention{}, false, fmt.Errorf("commit intervention completion: %w", err)
	}

	answeredAt := completion.OccurredAt.UTC()
	intervention.Status = execution.InterventionStatusAnswered
	intervention.Effect = completion.Effect
	intervention.UpdatedAt = answeredAt
	intervention.AnsweredAt = &answeredAt
	return intervention, true, nil
}
