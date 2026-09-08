package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type WorkflowStore struct {
	db *sql.DB
}

var _ workflow.Store = (*WorkflowStore)(nil)

func NewWorkflowStore(db *sql.DB) *WorkflowStore {
	return &WorkflowStore{db: db}
}

func (s *WorkflowStore) ApplyFeatureTransition(
	ctx context.Context,
	transition workflow.FeatureTransition,
) (workflow.Event, error) {
	if err := transition.Validate(); err != nil {
		return workflow.Event{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.Event{}, fmt.Errorf("begin feature transition: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	existing, found, err := findEventByIdempotencyKey(
		ctx,
		tx,
		transition.FeatureID,
		transition.IdempotencyKey,
	)
	if err != nil {
		return workflow.Event{}, err
	}
	if found {
		if err := matchTransition(existing, transition); err != nil {
			return workflow.Event{}, err
		}
		return existing, nil
	}

	var storedState string
	if err := tx.QueryRowContext(
		ctx,
		"SELECT state FROM features WHERE id = ?",
		transition.FeatureID,
	).Scan(&storedState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.Event{}, feature.ErrNotFound
		}
		return workflow.Event{}, fmt.Errorf(
			"select feature %q state: %w",
			transition.FeatureID,
			err,
		)
	}

	previousState := feature.State(storedState)
	payload, err := workflow.EncodeFeatureStateChangedPayload(
		previousState,
		transition.State,
	)
	if err != nil {
		return workflow.Event{}, err
	}

	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1
		 FROM workflow_events
		 WHERE aggregate_id = ?`,
		transition.FeatureID,
	).Scan(&sequence); err != nil {
		return workflow.Event{}, fmt.Errorf(
			"select next event sequence for feature %q: %w",
			transition.FeatureID,
			err,
		)
	}

	event := workflow.Event{
		ID:             transition.EventID,
		AggregateID:    transition.FeatureID,
		Type:           workflow.EventTypeFeatureStateChanged,
		Actor:          transition.Actor,
		OccurredAt:     transition.OccurredAt.UTC(),
		Sequence:       sequence,
		PayloadVersion: workflow.FeatureStateChangedPayloadVersion,
		IdempotencyKey: transition.IdempotencyKey,
		Payload:        payload,
	}
	if err := event.Validate(); err != nil {
		return workflow.Event{}, err
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE features
		 SET state = ?, updated_at = ?
		 WHERE id = ? AND state = ?`,
		transition.State,
		event.OccurredAt.Format(time.RFC3339Nano),
		transition.FeatureID,
		previousState,
	)
	if err != nil {
		return workflow.Event{}, fmt.Errorf(
			"update feature %q state: %w",
			transition.FeatureID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return workflow.Event{}, fmt.Errorf(
			"read updated row count for feature %q: %w",
			transition.FeatureID,
			err,
		)
	}
	if rowsAffected != 1 {
		return workflow.Event{}, fmt.Errorf(
			"update feature %q: expected one affected row, got %d",
			transition.FeatureID,
			rowsAffected,
		)
	}

	if err := insertWorkflowEvent(ctx, tx, event); err != nil {
		return workflow.Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return workflow.Event{}, fmt.Errorf("commit feature transition: %w", err)
	}

	return event, nil
}

func (s *WorkflowStore) ListEvents(
	ctx context.Context,
	aggregateID string,
) ([]workflow.Event, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, aggregate_id, event_type, actor_kind, actor_id,
		        occurred_at, sequence, payload_version, idempotency_key, payload
		 FROM workflow_events
		 WHERE aggregate_id = ?
		 ORDER BY sequence`,
		aggregateID,
	)
	if err != nil {
		return nil, fmt.Errorf("list events for aggregate %q: %w", aggregateID, err)
	}
	defer rows.Close()

	events := make([]workflow.Event, 0)
	for rows.Next() {
		event, err := scanWorkflowEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events for aggregate %q: %w", aggregateID, err)
	}

	return events, nil
}

func findEventByIdempotencyKey(
	ctx context.Context,
	tx *sql.Tx,
	aggregateID string,
	idempotencyKey string,
) (workflow.Event, bool, error) {
	event, err := scanWorkflowEvent(tx.QueryRowContext(
		ctx,
		`SELECT id, aggregate_id, event_type, actor_kind, actor_id,
		        occurred_at, sequence, payload_version, idempotency_key, payload
		 FROM workflow_events
		 WHERE aggregate_id = ? AND idempotency_key = ?`,
		aggregateID,
		idempotencyKey,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Event{}, false, nil
	}
	if err != nil {
		return workflow.Event{}, false, err
	}
	return event, true, nil
}

func matchTransition(
	event workflow.Event,
	transition workflow.FeatureTransition,
) error {
	payload, err := workflow.DecodeFeatureStateChangedPayload(
		event.PayloadVersion,
		event.Payload,
	)
	if err != nil {
		return err
	}

	if event.Type != workflow.EventTypeFeatureStateChanged ||
		event.Actor != transition.Actor ||
		payload.State != transition.State {
		return workflow.ErrIdempotencyConflict
	}

	return nil
}

func insertWorkflowEvent(
	ctx context.Context,
	tx *sql.Tx,
	event workflow.Event,
) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO workflow_events (
			id, aggregate_id, event_type, actor_kind, actor_id,
			occurred_at, sequence, payload_version, idempotency_key, payload
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.AggregateID,
		event.Type,
		event.Actor.Kind,
		event.Actor.ID,
		event.OccurredAt.Format(time.RFC3339Nano),
		event.Sequence,
		event.PayloadVersion,
		event.IdempotencyKey,
		event.Payload,
	)
	if err != nil {
		return fmt.Errorf("insert workflow event %q: %w", event.ID, err)
	}
	return nil
}

type eventScanner interface {
	Scan(dest ...any) error
}

func scanWorkflowEvent(scanner eventScanner) (workflow.Event, error) {
	event := workflow.Event{}
	var eventType string
	var actorKind string
	var occurredAt string

	if err := scanner.Scan(
		&event.ID,
		&event.AggregateID,
		&eventType,
		&actorKind,
		&event.Actor.ID,
		&occurredAt,
		&event.Sequence,
		&event.PayloadVersion,
		&event.IdempotencyKey,
		&event.Payload,
	); err != nil {
		return workflow.Event{}, err
	}

	event.Type = workflow.EventType(eventType)
	event.Actor.Kind = workflow.ActorKind(actorKind)

	parsedTime, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return workflow.Event{}, fmt.Errorf(
			"parse occurrence time for event %q: %w",
			event.ID,
			err,
		)
	}
	event.OccurredAt = parsedTime

	if err := event.Validate(); err != nil {
		return workflow.Event{}, err
	}

	return event, nil
}
