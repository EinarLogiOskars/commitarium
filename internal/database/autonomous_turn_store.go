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

// BeginAutonomousTurn atomically rotates an existing session to a new worker
// attempt and marks both the session and its run as running. This prevents a
// restart from seeing a new attempt cursor without the matching lifecycle
// state, or vice versa.
func (s *ExecutionStore) BeginAutonomousTurn(
	ctx context.Context,
	admission execution.AutonomousTurnAdmission,
) (bool, error) {
	if err := validateAutonomousTurnAdmission(admission); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin autonomous turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	session, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		admission.SessionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return false, execution.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("select session for autonomous turn: %w", err)
	}
	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason,
		        planning_round_limit, implementation_review_round_limit,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		session.RunID,
	))
	if err != nil {
		return false, fmt.Errorf("select run for autonomous turn: %w", err)
	}
	checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		session.ID,
	))
	if err != nil {
		return false, fmt.Errorf("select worker checkpoint for autonomous turn: %w", err)
	}

	// An exact retry after this transaction committed observes the new attempt.
	// It must not launch it again, regardless of whether it is still running or
	// has already returned to the waiting boundary.
	if checkpoint.AttemptID == admission.NextAttempt.AttemptID {
		return false, nil
	}
	expectedRunStatus := execution.RunStatusWaitingForUser
	if admission.RunAlreadyActive {
		expectedRunStatus = execution.RunStatusRunning
	}
	if session.Status != execution.SessionStatusWaitingForUser ||
		session.ProviderSessionID == "" || run.Status != expectedRunStatus {
		return false, execution.ErrStateConflict
	}
	var state string
	var acceptedGoal string
	var goalAcceptedAt sql.NullString
	if err := tx.QueryRowContext(
		ctx,
		`SELECT state, accepted_goal, goal_accepted_at FROM features WHERE id = ?`,
		run.FeatureID,
	).Scan(&state, &acceptedGoal, &goalAcceptedAt); err != nil {
		return false, fmt.Errorf("select feature planning boundary: %w", err)
	}
	if feature.State(state) != admission.ExpectedFeatureState ||
		strings.TrimSpace(acceptedGoal) == "" || !goalAcceptedAt.Valid {
		return false, execution.ErrStateConflict
	}
	if checkpoint.AttemptID != admission.PreviousAttemptID ||
		checkpoint.LastEventSequence != admission.PreviousLastEventSequence {
		return false, execution.ErrWorkerAttemptConflict
	}
	if admission.RunAlreadyActive {
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
			return false, fmt.Errorf("count active sessions for chained turn: %w", err)
		}
		if activeCount != 0 {
			return false, execution.ErrStateConflict
		}
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
		return false, fmt.Errorf("replace worker attempt for autonomous turn: %w", err)
	}
	if err := requireExecutionUpdate(result, "worker attempt", admission.PreviousAttemptID); err != nil {
		return false, err
	}

	result, err = tx.ExecContext(
		ctx,
		`UPDATE sessions SET status = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		execution.SessionStatusRunning, formatExecutionTime(admission.OccurredAt),
		session.ID, execution.SessionStatusWaitingForUser,
	)
	if err != nil {
		return false, fmt.Errorf("activate session for autonomous turn: %w", err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return false, err
	}

	result, err = tx.ExecContext(ctx,
		`UPDATE runs SET status = ?, reason = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		execution.RunStatusRunning, admission.RunReason,
		formatExecutionTime(admission.OccurredAt), run.ID, expectedRunStatus,
	)
	if err != nil {
		return false, fmt.Errorf("activate run for autonomous turn: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit autonomous turn: %w", err)
	}
	return true, nil
}

func validateAutonomousTurnAdmission(admission execution.AutonomousTurnAdmission) error {
	switch {
	case strings.TrimSpace(admission.SessionID) == "":
		return fmt.Errorf("%w: session ID is required", execution.ErrInvalidSession)
	case admission.NextAttempt.SessionID != admission.SessionID:
		return fmt.Errorf("%w: attempt must belong to the session", execution.ErrInvalidWorkerAttempt)
	case strings.TrimSpace(admission.PreviousAttemptID) == "":
		return fmt.Errorf("%w: previous attempt ID is required", execution.ErrInvalidWorkerAttempt)
	case admission.PreviousLastEventSequence < 0:
		return fmt.Errorf("%w: previous event sequence cannot be negative", execution.ErrInvalidWorkerAttempt)
	case admission.NextAttempt.AttemptID == admission.PreviousAttemptID:
		return fmt.Errorf("%w: replacement attempt must be new", execution.ErrInvalidWorkerAttempt)
	case strings.TrimSpace(admission.RunReason) == "":
		return fmt.Errorf("%w: run reason is required", execution.ErrInvalidRun)
	case !admission.ExpectedFeatureState.IsValid():
		return fmt.Errorf("%w: expected feature state is invalid", execution.ErrInvalidRun)
	case admission.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", execution.ErrInvalidStatusTransition)
	}
	return admission.NextAttempt.Validate()
}
