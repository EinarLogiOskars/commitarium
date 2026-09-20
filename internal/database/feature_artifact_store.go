package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

var _ workflow.ArtifactStore = (*WorkflowStore)(nil)

func (s *WorkflowStore) PutFeatureArtifact(
	ctx context.Context,
	mutation workflow.FeatureArtifactMutation,
) (workflow.FeatureArtifact, workflow.Event, error) {
	if err := mutation.Validate(); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, fmt.Errorf("begin feature artifact update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := findEventByIdempotencyKey(ctx, tx, mutation.FeatureID, mutation.IdempotencyKey)
	if err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, err
	}
	if found {
		payload, decodeErr := workflow.DecodeFeatureArtifactUpdatedPayload(existing.PayloadVersion, existing.Payload)
		if decodeErr != nil || existing.Type != workflow.EventTypeArtifactUpdated || existing.Actor != mutation.Actor ||
			payload.Kind != mutation.Kind || payload.ExpectedRevision != mutation.ExpectedRevision ||
			payload.SHA256 != mutation.DocumentDigest {
			return workflow.FeatureArtifact{}, workflow.Event{}, workflow.ErrIdempotencyConflict
		}
		artifact, getErr := getFeatureArtifactRevision(ctx, tx, mutation.FeatureID, mutation.Kind, payload.Revision)
		return artifact, existing, getErr
	}

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM features WHERE id = ?`, mutation.FeatureID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.FeatureArtifact{}, workflow.Event{}, feature.ErrNotFound
		}
		return workflow.FeatureArtifact{}, workflow.Event{}, fmt.Errorf("verify feature for artifact: %w", err)
	}

	currentRevision := 0
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM feature_artifacts WHERE feature_id = ? AND kind = ?`,
		mutation.FeatureID, mutation.Kind,
	).Scan(&currentRevision); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, fmt.Errorf("read current artifact revision: %w", err)
	}
	if currentRevision != mutation.ExpectedRevision {
		return workflow.FeatureArtifact{}, workflow.Event{}, workflow.ErrArtifactConflict
	}
	revision := currentRevision + 1
	artifact := workflow.FeatureArtifact{
		FeatureID: mutation.FeatureID, Kind: mutation.Kind, Revision: revision,
		Document: mutation.Document, Actor: mutation.Actor, UpdatedAt: mutation.OccurredAt.UTC(),
	}
	if err := artifact.Validate(); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO feature_artifacts (
			feature_id, kind, revision, document, actor_kind, actor_id, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		artifact.FeatureID, artifact.Kind, artifact.Revision, artifact.Document,
		artifact.Actor.Kind, artifact.Actor.ID, artifact.UpdatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, fmt.Errorf("insert feature artifact revision: %w", err)
	}

	var sequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM workflow_events WHERE aggregate_id = ?`,
		mutation.FeatureID,
	).Scan(&sequence); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, fmt.Errorf("select artifact event sequence: %w", err)
	}
	payload, err := workflow.EncodeFeatureArtifactUpdatedPayload(
		mutation.Kind, revision, mutation.ExpectedRevision, mutation.DocumentDigest,
	)
	if err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, err
	}
	event := workflow.Event{
		ID: mutation.EventID, AggregateID: mutation.FeatureID,
		Type: workflow.EventTypeArtifactUpdated, Actor: mutation.Actor,
		OccurredAt: mutation.OccurredAt.UTC(), Sequence: sequence,
		PayloadVersion: workflow.FeatureArtifactUpdatedPayloadVersion,
		IdempotencyKey: mutation.IdempotencyKey, Payload: payload,
	}
	if err := event.Validate(); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, err
	}
	if err := insertWorkflowEvent(ctx, tx, event); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return workflow.FeatureArtifact{}, workflow.Event{}, fmt.Errorf("commit feature artifact update: %w", err)
	}
	return artifact, event, nil
}

func (s *WorkflowStore) GetFeatureArtifact(
	ctx context.Context,
	featureID string,
	kind featureartifact.Kind,
) (workflow.FeatureArtifact, error) {
	if !kind.IsValid() {
		return workflow.FeatureArtifact{}, featureartifact.ErrInvalidArtifact
	}
	artifact, err := scanFeatureArtifact(s.db.QueryRowContext(ctx,
		`SELECT feature_id, kind, revision, document, actor_kind, actor_id, updated_at
		 FROM feature_artifacts
		 WHERE feature_id = ? AND kind = ?
		 ORDER BY revision DESC LIMIT 1`,
		featureID, kind,
	))
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if existsErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM features WHERE id = ?`, featureID).Scan(&exists); errors.Is(existsErr, sql.ErrNoRows) {
			return workflow.FeatureArtifact{}, feature.ErrNotFound
		} else if existsErr != nil {
			return workflow.FeatureArtifact{}, fmt.Errorf("verify feature for artifact: %w", existsErr)
		}
		return workflow.FeatureArtifact{}, workflow.ErrArtifactNotFound
	}
	if err != nil {
		return workflow.FeatureArtifact{}, fmt.Errorf("get latest feature artifact: %w", err)
	}
	return artifact, nil
}

func getFeatureArtifactRevision(
	ctx context.Context,
	tx *sql.Tx,
	featureID string,
	kind featureartifact.Kind,
	revision int,
) (workflow.FeatureArtifact, error) {
	artifact, err := scanFeatureArtifact(tx.QueryRowContext(ctx,
		`SELECT feature_id, kind, revision, document, actor_kind, actor_id, updated_at
		 FROM feature_artifacts WHERE feature_id = ? AND kind = ? AND revision = ?`,
		featureID, kind, revision,
	))
	if err != nil {
		return workflow.FeatureArtifact{}, fmt.Errorf("get feature artifact revision: %w", err)
	}
	return artifact, nil
}

func scanFeatureArtifact(scanner eventScanner) (workflow.FeatureArtifact, error) {
	artifact := workflow.FeatureArtifact{}
	var kind, actorKind, updatedAt string
	if err := scanner.Scan(
		&artifact.FeatureID, &kind, &artifact.Revision, &artifact.Document,
		&actorKind, &artifact.Actor.ID, &updatedAt,
	); err != nil {
		return workflow.FeatureArtifact{}, err
	}
	artifact.Kind = featureartifact.Kind(kind)
	artifact.Actor.Kind = workflow.ActorKind(actorKind)
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return workflow.FeatureArtifact{}, fmt.Errorf("parse feature artifact update time: %w", err)
	}
	artifact.UpdatedAt = parsed
	if err := artifact.Validate(); err != nil {
		return workflow.FeatureArtifact{}, err
	}
	return artifact, nil
}
