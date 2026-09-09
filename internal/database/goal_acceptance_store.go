package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

// AcceptGoal stores the immutable goal snapshot and its workflow event in one
// transaction. The waiting run/session checks make acceptance a boundary
// between completed clarification work and later workspace/planning work.
func (s *WorkflowStore) AcceptGoal(
	ctx context.Context,
	acceptance workflow.GoalAcceptance,
) (workflow.Event, error) {
	if err := acceptance.Validate(); err != nil {
		return workflow.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.Event{}, fmt.Errorf("begin goal acceptance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := findEventByIdempotencyKey(
		ctx, tx, acceptance.FeatureID, acceptance.IdempotencyKey,
	)
	if err != nil {
		return workflow.Event{}, err
	}
	if found {
		if err := matchGoalAcceptance(existing, acceptance); err != nil {
			return workflow.Event{}, err
		}
		return existing, nil
	}

	var featureState string
	var acceptedGoal string
	var acceptedAt sql.NullString
	var sessionStatus string
	var runStatus string
	err = tx.QueryRowContext(
		ctx,
		`SELECT f.state, f.accepted_goal, f.goal_accepted_at, s.status, r.status
		 FROM sessions s
		 JOIN runs r ON r.id = s.run_id
		 JOIN features f ON f.id = r.feature_id
		 WHERE s.id = ? AND f.id = ?`,
		acceptance.SessionID,
		acceptance.FeatureID,
	).Scan(&featureState, &acceptedGoal, &acceptedAt, &sessionStatus, &runStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Event{}, workflow.ErrGoalAcceptanceNotAllowed
	}
	if err != nil {
		return workflow.Event{}, fmt.Errorf("select goal acceptance boundary: %w", err)
	}
	if acceptedGoal != "" || acceptedAt.Valid {
		return workflow.Event{}, workflow.ErrGoalAlreadyAccepted
	}
	if feature.State(featureState) != feature.StateDraft ||
		execution.SessionStatus(sessionStatus) != execution.SessionStatusWaitingForUser ||
		execution.RunStatus(runStatus) != execution.RunStatusWaitingForUser {
		return workflow.Event{}, workflow.ErrGoalAcceptanceNotAllowed
	}

	payload, err := workflow.EncodeGoalAcceptedPayload(
		acceptance.Goal, acceptance.SessionID,
	)
	if err != nil {
		return workflow.Event{}, err
	}
	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1
		 FROM workflow_events WHERE aggregate_id = ?`,
		acceptance.FeatureID,
	).Scan(&sequence); err != nil {
		return workflow.Event{}, fmt.Errorf("select goal acceptance event sequence: %w", err)
	}
	event := workflow.Event{
		ID: acceptance.EventID, AggregateID: acceptance.FeatureID,
		Type: workflow.EventTypeGoalAccepted, Actor: acceptance.Actor,
		OccurredAt: acceptance.OccurredAt.UTC(), Sequence: sequence,
		PayloadVersion: workflow.GoalAcceptedPayloadVersion,
		IdempotencyKey: acceptance.IdempotencyKey, Payload: payload,
	}
	if err := event.Validate(); err != nil {
		return workflow.Event{}, err
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE features
		 SET accepted_goal = ?, goal_accepted_at = ?, updated_at = ?
		 WHERE id = ? AND state = ? AND accepted_goal = '' AND goal_accepted_at IS NULL
		   AND EXISTS (
		       SELECT 1 FROM sessions s
		       JOIN runs r ON r.id = s.run_id
		       WHERE s.id = ? AND r.feature_id = features.id
		         AND s.status = ? AND r.status = ?
		   )`,
		acceptance.Goal,
		formatExecutionTime(event.OccurredAt),
		formatExecutionTime(event.OccurredAt),
		acceptance.FeatureID,
		feature.StateDraft,
		acceptance.SessionID,
		execution.SessionStatusWaitingForUser,
		execution.RunStatusWaitingForUser,
	)
	if err != nil {
		return workflow.Event{}, fmt.Errorf("store accepted goal: %w", err)
	}
	if err := requireExecutionUpdate(result, "feature goal", acceptance.FeatureID); err != nil {
		return workflow.Event{}, workflow.ErrGoalAcceptanceNotAllowed
	}
	if err := insertWorkflowEvent(ctx, tx, event); err != nil {
		return workflow.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return workflow.Event{}, fmt.Errorf("commit goal acceptance: %w", err)
	}
	return event, nil
}

func matchGoalAcceptance(
	event workflow.Event,
	acceptance workflow.GoalAcceptance,
) error {
	if event.Type != workflow.EventTypeGoalAccepted || event.Actor != acceptance.Actor {
		return workflow.ErrIdempotencyConflict
	}
	payload, err := workflow.DecodeGoalAcceptedPayload(event.PayloadVersion, event.Payload)
	if err != nil {
		return err
	}
	if payload.Goal != acceptance.Goal || payload.SessionID != acceptance.SessionID {
		return workflow.ErrIdempotencyConflict
	}
	return nil
}
