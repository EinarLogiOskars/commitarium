package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

// BeginParallelImplementationTurns atomically activates exactly the lead and
// reviewer sessions. It is intentionally specific rather than a general
// parallel workflow primitive.
func (s *ExecutionStore) BeginParallelImplementationTurns(
	ctx context.Context,
	admission execution.ParallelImplementationAdmission,
) (bool, error) {
	if strings.TrimSpace(admission.RunID) == "" || strings.TrimSpace(admission.RunReason) == "" || admission.OccurredAt.IsZero() ||
		admission.Lead.SessionID == admission.Reviewer.SessionID {
		return false, execution.ErrInvalidRun
	}
	if err := validateAutonomousTurnAdmission(admission.Lead); err != nil {
		return false, err
	}
	if err := validateAutonomousTurnAdmission(admission.Reviewer); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin parallel implementation turns: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := scanExecutionRun(tx.QueryRowContext(ctx,
		`SELECT id, feature_id, status, reason, wait_kind, paused, paused_from_wait_kind,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider, lead_model, reviewer_model, merge_policy, autonomy_policy, independent_acceptance_tests, plan_version,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`, admission.RunID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, execution.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("select run for parallel implementation: %w", err)
	}
	if run.Status == execution.RunStatusRunning {
		leadCheckpoint, leadErr := scanWorkerAttemptCheckpoint(tx.QueryRowContext(ctx,
			`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at FROM worker_attempt_checkpoints WHERE session_id = ?`,
			admission.Lead.SessionID))
		reviewerCheckpoint, reviewerErr := scanWorkerAttemptCheckpoint(tx.QueryRowContext(ctx,
			`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at FROM worker_attempt_checkpoints WHERE session_id = ?`,
			admission.Reviewer.SessionID))
		if leadErr == nil && reviewerErr == nil &&
			leadCheckpoint.AttemptID == admission.Lead.NextAttempt.AttemptID &&
			reviewerCheckpoint.AttemptID == admission.Reviewer.NextAttempt.AttemptID {
			return false, nil
		}
	}
	if run.Paused || run.Status != execution.RunStatusWaitingForUser || !run.IndependentAcceptanceTests {
		return false, execution.ErrStateConflict
	}
	var state string
	var acceptedGoal string
	var goalAcceptedAt sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT state, accepted_goal, goal_accepted_at FROM features WHERE id = ?`, run.FeatureID,
	).Scan(&state, &acceptedGoal, &goalAcceptedAt); err != nil {
		return false, fmt.Errorf("select feature for parallel implementation: %w", err)
	}
	if feature.State(state) != feature.StateImplementing || strings.TrimSpace(acceptedGoal) == "" || !goalAcceptedAt.Valid {
		return false, execution.ErrStateConflict
	}

	for _, turn := range []execution.AutonomousTurnAdmission{admission.Lead, admission.Reviewer} {
		session, err := scanExecutionSession(tx.QueryRowContext(ctx,
			`SELECT id, run_id, agent_id, role, status, provider_session_id,
			        outcome, disposition, summary, recovery_attempt, started_at, updated_at, ended_at
			 FROM sessions WHERE id = ?`, turn.SessionID))
		if err != nil {
			return false, fmt.Errorf("select parallel implementation session: %w", err)
		}
		checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(ctx,
			`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
			 FROM worker_attempt_checkpoints WHERE session_id = ?`, turn.SessionID))
		if err != nil {
			return false, fmt.Errorf("select parallel implementation checkpoint: %w", err)
		}
		if checkpoint.AttemptID == turn.NextAttempt.AttemptID {
			return false, nil
		}
		if session.RunID != run.ID || session.Status != execution.SessionStatusWaitingForUser || session.ProviderSessionID == "" ||
			checkpoint.AttemptID != turn.PreviousAttemptID || checkpoint.LastEventSequence != turn.PreviousLastEventSequence {
			return false, execution.ErrStateConflict
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE worker_attempt_checkpoints
			 SET attempt_id = ?, last_event_sequence = 0, created_at = ?, updated_at = ?
			 WHERE session_id = ? AND attempt_id = ? AND last_event_sequence = ?`,
			turn.NextAttempt.AttemptID, formatExecutionTime(admission.OccurredAt), formatExecutionTime(admission.OccurredAt),
			turn.SessionID, turn.PreviousAttemptID, turn.PreviousLastEventSequence)
		if err != nil {
			return false, fmt.Errorf("replace parallel implementation checkpoint: %w", err)
		}
		if err := requireExecutionUpdate(result, "worker attempt", turn.PreviousAttemptID); err != nil {
			return false, err
		}
		result, err = tx.ExecContext(ctx,
			`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
			execution.SessionStatusRunning, formatExecutionTime(admission.OccurredAt), turn.SessionID,
			execution.SessionStatusWaitingForUser)
		if err != nil {
			return false, fmt.Errorf("activate parallel implementation session: %w", err)
		}
		if err := requireExecutionUpdate(result, "session", turn.SessionID); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE runs SET status = ?, reason = ?, wait_kind = '', paused_from_wait_kind = '', updated_at = ?
		 WHERE id = ? AND status = ?`,
		execution.RunStatusRunning, admission.RunReason, formatExecutionTime(admission.OccurredAt), run.ID,
		execution.RunStatusWaitingForUser)
	if err != nil {
		return false, fmt.Errorf("activate parallel implementation run: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit parallel implementation turns: %w", err)
	}
	return true, nil
}
