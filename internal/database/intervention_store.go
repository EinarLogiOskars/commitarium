package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func (s *ExecutionStore) QueueIntervention(
	ctx context.Context,
	request execution.InterventionRequest,
) (execution.InterventionRequestResult, bool, error) {
	if err := request.Validate(); err != nil {
		return execution.InterventionRequestResult{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("begin intervention: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := findIntervention(ctx, tx, request.ID)
	if err != nil {
		return execution.InterventionRequestResult{}, false, err
	}
	if found {
		if existing.RunID != request.RunID || existing.Target != request.Target ||
			existing.Message != request.Message {
			return execution.InterventionRequestResult{}, false, execution.ErrInterventionConflict
		}
		event, eventFound, err := findExecutionEvent(ctx, tx, interventionEventID(existing.ID))
		if err != nil {
			return execution.InterventionRequestResult{}, false, err
		}
		if !eventFound || event.SessionID != existing.SessionID ||
			event.Type != worker.EventUserMessage || event.Text != existing.Message {
			return execution.InterventionRequestResult{}, false, execution.ErrInterventionConflict
		}
		return execution.InterventionRequestResult{Intervention: existing, UserEvent: event}, false, nil
	}

	run, err := scanExecutionRun(tx.QueryRowContext(
		ctx,
		`SELECT id, feature_id, status, reason, wait_kind, paused, paused_from_wait_kind,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider, merge_policy, autonomy_policy, plan_version,
		        started_at, updated_at, ended_at
		 FROM runs WHERE id = ?`,
		request.RunID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.InterventionRequestResult{}, false, execution.ErrNotFound
	}
	if err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("select run %q for intervention: %w", request.RunID, err)
	}
	if run.Status.IsTerminal() {
		return execution.InterventionRequestResult{}, false, execution.ErrInterventionNotAllowed
	}

	target, err := selectInterventionTarget(ctx, tx, run.ID, request.Target)
	if err != nil {
		return execution.InterventionRequestResult{}, false, err
	}
	if _, found, err := findUnfinishedIntervention(ctx, tx, run.ID); err != nil {
		return execution.InterventionRequestResult{}, false, err
	} else if found {
		return execution.InterventionRequestResult{}, false, execution.ErrInterventionInProgress
	}

	status := execution.InterventionStatusWaitingForBoundary
	if run.Status == execution.RunStatusWaitingForUser {
		status = execution.InterventionStatusQueued
	}
	now := request.OccurredAt.UTC()
	intervention := execution.Intervention{
		ID: request.ID, RunID: run.ID, SessionID: target.SessionID,
		Target: request.Target, Message: request.Message, Status: status,
		RequestedAt: now, UpdatedAt: now,
	}
	if run.Status == execution.RunStatusWaitingForUser {
		intervention.ResumeReason = run.Reason
		priorReason, inherited, err := unresolvedInterventionResumeReason(ctx, tx, run.ID)
		if err != nil {
			return execution.InterventionRequestResult{}, false, err
		}
		if inherited && priorReason != "" {
			intervention.ResumeReason = priorReason
		}
	}
	if err := intervention.Validate(); err != nil {
		return execution.InterventionRequestResult{}, false, err
	}

	if !run.Paused {
		run.Paused = true
		if run.Status == execution.RunStatusWaitingForUser {
			run.PausedFromWaitKind = run.WaitKind
		}
	}
	run.WaitKind = execution.RunWaitKindPaused
	run.UpdatedAt = now
	if err := run.Validate(); err != nil {
		return execution.InterventionRequestResult{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE runs
		 SET wait_kind = ?, paused = ?, paused_from_wait_kind = ?, updated_at = ?
		 WHERE id = ?`,
		run.WaitKind, run.Paused, run.PausedFromWaitKind,
		formatExecutionTime(run.UpdatedAt), run.ID,
	); err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("arm pause for intervention %q: %w", intervention.ID, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO run_interventions (
			id, run_id, session_id, target_role, message, status,
			attempt_id, effect, resume_reason, resolution_action_id,
			requested_at, updated_at, answered_at, resolved_at
		 ) VALUES (?, ?, ?, ?, ?, ?, '', '', ?, '', ?, ?, NULL, NULL)`,
		intervention.ID, intervention.RunID, intervention.SessionID,
		intervention.Target, intervention.Message, intervention.Status,
		intervention.ResumeReason,
		formatExecutionTime(intervention.RequestedAt), formatExecutionTime(intervention.UpdatedAt),
	); err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("insert intervention %q: %w", intervention.ID, err)
	}

	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1
		 FROM session_events WHERE session_id = ?`,
		intervention.SessionID,
	).Scan(&sequence); err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("select intervention event sequence: %w", err)
	}
	event := execution.Event{
		ID: interventionEventID(intervention.ID), SessionID: intervention.SessionID,
		Sequence: sequence, Type: worker.EventUserMessage,
		Text: intervention.Message, OccurredAt: now,
	}
	if err := event.Validate(); err != nil {
		return execution.InterventionRequestResult{}, false, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO session_events (
			id, session_id, sequence, event_type, text, occurred_at,
			worker_attempt_id, worker_event_sequence
		 ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL)`,
		event.ID, event.SessionID, event.Sequence, event.Type, event.Text,
		formatExecutionTime(event.OccurredAt),
	); err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("insert intervention event %q: %w", event.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return execution.InterventionRequestResult{}, false, fmt.Errorf("commit intervention %q: %w", intervention.ID, err)
	}
	return execution.InterventionRequestResult{Intervention: intervention, UserEvent: event}, true, nil
}

func (s *ExecutionStore) GetLatestIntervention(
	ctx context.Context,
	runID string,
) (execution.Intervention, error) {
	intervention, err := scanIntervention(s.db.QueryRowContext(
		ctx,
		`SELECT id, run_id, session_id, target_role, message, status,
		        attempt_id, effect, resume_reason, resolution_action_id,
		        requested_at, updated_at, answered_at, resolved_at
		 FROM run_interventions
		 WHERE run_id = ?
		 ORDER BY requested_at DESC, id DESC
		 LIMIT 1`,
		runID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Intervention{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Intervention{}, fmt.Errorf("select latest intervention for run %q: %w", runID, err)
	}
	return intervention, nil
}

func (s *ExecutionStore) ListInterventionTargets(
	ctx context.Context,
	runID string,
) ([]execution.InterventionTarget, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status.IsTerminal() {
		return []execution.InterventionTarget{}, nil
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, role
		 FROM sessions
		 WHERE run_id = ?
		   AND role IN ('lead', 'reviewer')
		   AND status NOT IN ('completed', 'stopped', 'failed')
		 ORDER BY CASE role WHEN 'lead' THEN 0 ELSE 1 END, id`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list intervention targets for run %q: %w", runID, err)
	}
	defer rows.Close()
	targets := make([]execution.InterventionTarget, 0, 2)
	for rows.Next() {
		var target execution.InterventionTarget
		if err := rows.Scan(&target.SessionID, &target.Role); err != nil {
			return nil, fmt.Errorf("scan intervention target for run %q: %w", runID, err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate intervention targets for run %q: %w", runID, err)
	}
	return targets, nil
}

func selectInterventionTarget(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	role worker.Role,
) (execution.InterventionTarget, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT id
		 FROM sessions
		 WHERE run_id = ? AND role = ?
		   AND status NOT IN ('completed', 'stopped', 'failed')
		 ORDER BY id`,
		runID, role,
	)
	if err != nil {
		return execution.InterventionTarget{}, fmt.Errorf("select %s intervention target: %w", role, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return execution.InterventionTarget{}, fmt.Errorf("scan %s intervention target: %w", role, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return execution.InterventionTarget{}, fmt.Errorf("iterate %s intervention targets: %w", role, err)
	}
	if len(ids) != 1 {
		return execution.InterventionTarget{}, execution.ErrInterventionTargetUnavailable
	}
	return execution.InterventionTarget{Role: role, SessionID: ids[0]}, nil
}

func findIntervention(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (execution.Intervention, bool, error) {
	intervention, err := scanIntervention(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, session_id, target_role, message, status,
		        attempt_id, effect, resume_reason, resolution_action_id,
		        requested_at, updated_at, answered_at, resolved_at
		 FROM run_interventions WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Intervention{}, false, nil
	}
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("select intervention %q: %w", id, err)
	}
	return intervention, true, nil
}

func findUnfinishedIntervention(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
) (execution.Intervention, bool, error) {
	intervention, err := scanIntervention(tx.QueryRowContext(
		ctx,
		`SELECT id, run_id, session_id, target_role, message, status,
		        attempt_id, effect, resume_reason, resolution_action_id,
		        requested_at, updated_at, answered_at, resolved_at
		 FROM run_interventions
		 WHERE run_id = ? AND status != 'answered'
		 LIMIT 1`,
		runID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Intervention{}, false, nil
	}
	if err != nil {
		return execution.Intervention{}, false, fmt.Errorf("select unfinished intervention for run %q: %w", runID, err)
	}
	return intervention, true, nil
}

// unresolvedInterventionResumeReason carries the original workflow checkpoint
// through a multi-message intervention conversation. Only the newest request
// matters: once that request is resolved, older unanswered classifications must
// not pull a later independent intervention back to a stale checkpoint.
func unresolvedInterventionResumeReason(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
) (string, bool, error) {
	var status execution.InterventionStatus
	var reason string
	var resolvedAt sql.NullString
	err := tx.QueryRowContext(
		ctx,
		`SELECT status, resume_reason, resolved_at
		 FROM run_interventions
		 WHERE run_id = ?
		 ORDER BY requested_at DESC, id DESC
		 LIMIT 1`,
		runID,
	).Scan(&status, &reason, &resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select prior intervention checkpoint for run %q: %w", runID, err)
	}
	return reason, status == execution.InterventionStatusAnswered && !resolvedAt.Valid, nil
}

func scanIntervention(scanner executionScanner) (execution.Intervention, error) {
	intervention := execution.Intervention{}
	var target string
	var status string
	var requestedAt string
	var updatedAt string
	var answeredAt sql.NullString
	var resolvedAt sql.NullString
	if err := scanner.Scan(
		&intervention.ID, &intervention.RunID, &intervention.SessionID,
		&target, &intervention.Message, &status,
		&intervention.AttemptID, &intervention.Effect, &intervention.ResumeReason,
		&intervention.ResolutionActionID,
		&requestedAt, &updatedAt, &answeredAt, &resolvedAt,
	); err != nil {
		return execution.Intervention{}, err
	}
	intervention.Target = worker.Role(target)
	intervention.Status = execution.InterventionStatus(status)
	var err error
	intervention.RequestedAt, err = parseExecutionTime(requestedAt)
	if err != nil {
		return execution.Intervention{}, fmt.Errorf("parse intervention %q request time: %w", intervention.ID, err)
	}
	intervention.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return execution.Intervention{}, fmt.Errorf("parse intervention %q update time: %w", intervention.ID, err)
	}
	intervention.AnsweredAt, err = parseOptionalExecutionTime(answeredAt)
	if err != nil {
		return execution.Intervention{}, fmt.Errorf("parse intervention %q answer time: %w", intervention.ID, err)
	}
	intervention.ResolvedAt, err = parseOptionalExecutionTime(resolvedAt)
	if err != nil {
		return execution.Intervention{}, fmt.Errorf("parse intervention %q resolution time: %w", intervention.ID, err)
	}
	if err := intervention.Validate(); err != nil {
		return execution.Intervention{}, err
	}
	return intervention, nil
}

func interventionEventID(interventionID string) string {
	return interventionID + ":user-message"
}
