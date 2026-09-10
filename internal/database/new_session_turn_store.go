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

// BeginNewSessionTurn atomically creates a new logical agent conversation,
// binds its first worker attempt, and marks the existing run active.
func (s *ExecutionStore) BeginNewSessionTurn(
	ctx context.Context,
	admission execution.NewSessionTurnAdmission,
) (bool, error) {
	if err := validateNewSessionTurnAdmission(admission); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin new session turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		admission.Session.ID,
	))
	if err == nil {
		checkpoint, checkpointErr := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
			ctx,
			`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
			 FROM worker_attempt_checkpoints WHERE session_id = ?`,
			existing.ID,
		))
		if checkpointErr != nil {
			return false, fmt.Errorf("select existing session attempt: %w", checkpointErr)
		}
		if existing.RunID != admission.Session.RunID ||
			existing.AgentID != admission.Session.AgentID ||
			existing.Role != admission.Session.Role ||
			checkpoint.AttemptID != admission.Attempt.AttemptID {
			return false, execution.ErrRecordConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("select existing new session: %w", err)
	}

	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason,
		        planning_round_limit, implementation_review_round_limit,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		admission.Session.RunID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return false, execution.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("select run for new session: %w", err)
	}
	if run.Status != execution.RunStatusWaitingForUser {
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
		return false, fmt.Errorf("select feature for new session: %w", err)
	}
	if feature.State(state) != feature.StatePlanning ||
		strings.TrimSpace(acceptedGoal) == "" || !goalAcceptedAt.Valid {
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
		return false, fmt.Errorf("count active sessions for new turn: %w", err)
	}
	if activeCount != 0 {
		return false, execution.ErrStateConflict
	}

	session := admission.Session
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO sessions (
			id, run_id, agent_id, role, status, provider_session_id,
			outcome, disposition, summary, recovery_attempt,
			started_at, updated_at, ended_at
		 ) VALUES (?, ?, ?, ?, ?, '', '', '', '', 0, ?, ?, NULL)`,
		session.ID, session.RunID, session.AgentID, session.Role, session.Status,
		formatExecutionTime(session.StartedAt), formatExecutionTime(session.UpdatedAt),
	); err != nil {
		return false, fmt.Errorf("insert new session: %w", err)
	}

	attempt := admission.Attempt
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO worker_attempt_checkpoints (
			session_id, attempt_id, last_event_sequence, created_at, updated_at
		 ) VALUES (?, ?, 0, ?, ?)`,
		attempt.SessionID, attempt.AttemptID,
		formatExecutionTime(attempt.CreatedAt), formatExecutionTime(attempt.UpdatedAt),
	); err != nil {
		return false, fmt.Errorf("insert new session worker attempt: %w", err)
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE runs SET status = ?, reason = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		execution.RunStatusRunning, admission.RunReason,
		formatExecutionTime(admission.OccurredAt), run.ID,
		execution.RunStatusWaitingForUser,
	)
	if err != nil {
		return false, fmt.Errorf("activate run for new session: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit new session turn: %w", err)
	}
	return true, nil
}

func validateNewSessionTurnAdmission(admission execution.NewSessionTurnAdmission) error {
	switch {
	case admission.Session.Status != execution.SessionStatusStarting:
		return fmt.Errorf("%w: new session must start in starting state", execution.ErrInvalidSession)
	case admission.Attempt.SessionID != admission.Session.ID:
		return fmt.Errorf("%w: attempt must belong to new session", execution.ErrInvalidWorkerAttempt)
	case strings.TrimSpace(admission.RunReason) == "":
		return fmt.Errorf("%w: run reason is required", execution.ErrInvalidRun)
	case admission.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurrence time is required", execution.ErrInvalidStatusTransition)
	}
	if err := admission.Session.Validate(); err != nil {
		return err
	}
	return admission.Attempt.Validate()
}
