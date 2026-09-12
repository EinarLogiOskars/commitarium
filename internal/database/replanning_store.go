package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// BeginReplanningTurn makes the scope-change decision, plan-version advance,
// stale merge-approval removal, and next provider attempt one durable fact.
// The feature transition itself is recorded first through workflow.Store; an
// interruption between the two operations is conservative and retryable.
func (s *ExecutionStore) BeginReplanningTurn(
	ctx context.Context,
	admission execution.ReplanningTurnAdmission,
) (execution.Run, execution.PlanRevision, bool, error) {
	if err := admission.Validate(); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("begin replanning turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var storedRunID string
	var storedAction execution.RunPauseAction
	err = tx.QueryRowContext(
		ctx, `SELECT run_id, action FROM run_actions WHERE id = ?`, admission.ID,
	).Scan(&storedRunID, &storedAction)
	if err == nil {
		if storedRunID != admission.RunID || storedAction != execution.RunPauseActionResume {
			return execution.Run{}, execution.PlanRevision{}, false, execution.ErrRunActionConflict
		}
		revision, revisionErr := getPlanRevision(ctx, tx, admission.RunID, admission.PlanVersion)
		if revisionErr != nil || !samePlanRevisionAdmission(revision, admission) {
			return execution.Run{}, execution.PlanRevision{}, false, execution.ErrRunActionConflict
		}
		intervention, found, findErr := findIntervention(ctx, tx, admission.InterventionID)
		if findErr != nil {
			return execution.Run{}, execution.PlanRevision{}, false, findErr
		}
		if !found || intervention.RunID != admission.RunID || intervention.ResolvedAt == nil ||
			intervention.ResolutionActionID != admission.ID {
			return execution.Run{}, execution.PlanRevision{}, false, execution.ErrRunActionConflict
		}
		run, runErr := scanRunForPause(ctx, tx, admission.RunID)
		return run, revision, false, runErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("find replanning action: %w", err)
	}

	intervention, found, err := findIntervention(ctx, tx, admission.InterventionID)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}
	if !found {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrNotFound
	}
	if intervention.RunID != admission.RunID ||
		intervention.Status != execution.InterventionStatusAnswered ||
		intervention.Effect != worker.InterventionEffectReplanningRequired ||
		intervention.ResolvedAt != nil {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
	}
	var latestID string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT id FROM run_interventions
		 WHERE run_id = ? ORDER BY requested_at DESC, id DESC LIMIT 1`,
		admission.RunID,
	).Scan(&latestID); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("select latest replanning intervention: %w", err)
	}
	if latestID != intervention.ID {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
	}

	run, err := scanRunForPause(ctx, tx, admission.RunID)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}
	if run.Status != execution.RunStatusWaitingForUser || !run.Paused ||
		run.WaitKind != execution.RunWaitKindPaused || run.PlanVersion+1 != admission.PlanVersion {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
	}
	var featureState feature.State
	var acceptedGoal string
	var goalAcceptedAt sql.NullString
	if err := tx.QueryRowContext(
		ctx, `SELECT state, accepted_goal, goal_accepted_at FROM features WHERE id = ?`, run.FeatureID,
	).Scan(&featureState, &acceptedGoal, &goalAcceptedAt); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("select feature for replanning: %w", err)
	}
	if featureState != feature.StatePlanning || strings.TrimSpace(acceptedGoal) == "" || !goalAcceptedAt.Valid {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
	}

	var priorVersion int
	var priorType worker.EventType
	if err := tx.QueryRowContext(
		ctx,
		`SELECT pm.plan_version, se.event_type
		 FROM planning_messages pm
		 JOIN session_events se ON se.id = pm.session_event_id
		 WHERE pm.run_id = ? AND pm.session_event_id = ?`,
		run.ID, admission.PreviousPlanEventID,
	).Scan(&priorVersion, &priorType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
		}
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("select previous plan for replanning: %w", err)
	}
	if priorVersion != run.PlanVersion || priorType != worker.EventPlanSubmitted {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
	}

	session, err := scanExecutionSession(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`,
		admission.NextAttempt.SessionID,
	))
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("select lead for replanning: %w", err)
	}
	if session.RunID != run.ID || session.Role != worker.RoleLead ||
		session.Status != execution.SessionStatusWaitingForUser || session.ProviderSessionID == "" {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
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
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("count active sessions for replanning: %w", err)
	}
	if activeCount != 0 {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrStateConflict
	}
	checkpoint, err := scanWorkerAttemptCheckpoint(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, last_event_sequence, created_at, updated_at
		 FROM worker_attempt_checkpoints WHERE session_id = ?`,
		session.ID,
	))
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("select lead checkpoint for replanning: %w", err)
	}
	if checkpoint.AttemptID != admission.PreviousAttemptID ||
		checkpoint.LastEventSequence != admission.PreviousLastEventSequence {
		return execution.Run{}, execution.PlanRevision{}, false, execution.ErrWorkerAttemptConflict
	}

	revision := execution.PlanRevision{
		RunID: run.ID, Version: admission.PlanVersion,
		InterventionID: intervention.ID, PreviousPlanEventID: admission.PreviousPlanEventID,
		EffectiveGoal: admission.EffectiveGoal, BaselineCommitID: admission.BaselineCommitID,
		CreatedAt: admission.OccurredAt.UTC(),
	}
	if err := revision.Validate(); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO run_plan_revisions (
		    run_id, version, intervention_id, previous_plan_event_id,
		    effective_goal, baseline_commit_id, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		revision.RunID, revision.Version, revision.InterventionID,
		revision.PreviousPlanEventID, revision.EffectiveGoal,
		revision.BaselineCommitID, formatExecutionTime(revision.CreatedAt),
	); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("insert plan revision: %w", err)
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE run_interventions
		 SET resolved_at = ?, resolution_action_id = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND effect = ? AND resolved_at IS NULL`,
		formatExecutionTime(revision.CreatedAt), admission.ID, formatExecutionTime(revision.CreatedAt),
		intervention.ID, execution.InterventionStatusAnswered,
		worker.InterventionEffectReplanningRequired,
	)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("resolve replanning intervention: %w", err)
	}
	if err := requireExecutionUpdate(result, "intervention", intervention.ID); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}

	next := admission.NextAttempt
	result, err = tx.ExecContext(
		ctx,
		`UPDATE worker_attempt_checkpoints
		 SET attempt_id = ?, last_event_sequence = 0, created_at = ?, updated_at = ?
		 WHERE session_id = ? AND attempt_id = ? AND last_event_sequence = ?`,
		next.AttemptID, formatExecutionTime(next.CreatedAt), formatExecutionTime(next.UpdatedAt),
		next.SessionID, admission.PreviousAttemptID, admission.PreviousLastEventSequence,
	)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("replace lead attempt for replanning: %w", err)
	}
	if err := requireExecutionUpdate(result, "worker attempt", admission.PreviousAttemptID); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}

	result, err = tx.ExecContext(
		ctx,
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		execution.SessionStatusRunning, formatExecutionTime(revision.CreatedAt),
		session.ID, execution.SessionStatusWaitingForUser,
	)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("activate lead for replanning: %w", err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}

	result, err = tx.ExecContext(
		ctx,
		`UPDATE feature_workspaces
		 SET approved_commit_id = '', merge_ready_at = NULL, updated_at = ?
		 WHERE feature_id = ? AND merge_commit_id = '' AND merged_at IS NULL`,
		formatExecutionTime(revision.CreatedAt), run.FeatureID,
	)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("clear stale merge approval for replanning: %w", err)
	}
	if err := requireExecutionUpdate(result, "workspace", run.FeatureID); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}

	run.Status = execution.RunStatusRunning
	run.Reason = admission.RunReason
	run.WaitKind = ""
	run.Paused = false
	run.PausedFromWaitKind = ""
	run.PlanVersion = revision.Version
	run.UpdatedAt = revision.CreatedAt
	if err := run.Validate(); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}
	result, err = tx.ExecContext(
		ctx,
		`UPDATE runs
		 SET status = ?, reason = ?, wait_kind = '', paused = 0,
		     paused_from_wait_kind = '', plan_version = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND paused = 1 AND wait_kind = ? AND plan_version = ?`,
		run.Status, run.Reason, run.PlanVersion, formatExecutionTime(run.UpdatedAt), run.ID,
		execution.RunStatusWaitingForUser, execution.RunWaitKindPaused, run.PlanVersion-1,
	)
	if err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("activate replanning run: %w", err)
	}
	if err := requireExecutionUpdate(result, "run", run.ID); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO run_actions (id, run_id, action, occurred_at) VALUES (?, ?, ?, ?)`,
		admission.ID, run.ID, execution.RunPauseActionResume,
		formatExecutionTime(revision.CreatedAt),
	); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("record replanning action: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return execution.Run{}, execution.PlanRevision{}, false, fmt.Errorf("commit replanning turn: %w", err)
	}
	return run, revision, true, nil
}

func (s *ExecutionStore) GetPlanRevision(
	ctx context.Context,
	runID string,
	version int,
) (execution.PlanRevision, error) {
	revision, err := getPlanRevision(ctx, s.db, runID, version)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.PlanRevision{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.PlanRevision{}, fmt.Errorf("get plan revision %q version %d: %w", runID, version, err)
	}
	return revision, nil
}

type planRevisionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getPlanRevision(
	ctx context.Context,
	querier planRevisionQuerier,
	runID string,
	version int,
) (execution.PlanRevision, error) {
	var revision execution.PlanRevision
	var createdAt string
	if err := querier.QueryRowContext(
		ctx,
		`SELECT run_id, version, intervention_id, previous_plan_event_id,
		        effective_goal, baseline_commit_id, created_at
		 FROM run_plan_revisions WHERE run_id = ? AND version = ?`,
		runID, version,
	).Scan(
		&revision.RunID, &revision.Version, &revision.InterventionID,
		&revision.PreviousPlanEventID, &revision.EffectiveGoal,
		&revision.BaselineCommitID, &createdAt,
	); err != nil {
		return execution.PlanRevision{}, err
	}
	var err error
	revision.CreatedAt, err = parseExecutionTime(createdAt)
	if err != nil {
		return execution.PlanRevision{}, fmt.Errorf("parse plan revision time: %w", err)
	}
	if err := revision.Validate(); err != nil {
		return execution.PlanRevision{}, err
	}
	return revision, nil
}

func samePlanRevisionAdmission(
	revision execution.PlanRevision,
	admission execution.ReplanningTurnAdmission,
) bool {
	return revision.RunID == admission.RunID && revision.Version == admission.PlanVersion &&
		revision.InterventionID == admission.InterventionID &&
		revision.PreviousPlanEventID == admission.PreviousPlanEventID &&
		revision.EffectiveGoal == admission.EffectiveGoal &&
		revision.BaselineCommitID == admission.BaselineCommitID
}
