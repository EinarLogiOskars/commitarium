package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type WorkspacePublicationStore struct {
	db *sql.DB
}

var _ workspace.PublicationStore = (*WorkspacePublicationStore)(nil)

func NewWorkspacePublicationStore(db *sql.DB) *WorkspacePublicationStore {
	return &WorkspacePublicationStore{db: db}
}

func (store *WorkspacePublicationStore) GetPublication(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (workspace.Publication, error) {
	return scanPublication(store.db.QueryRowContext(ctx, `
		SELECT id, run_id, workspace_id, idempotency_key, commit_message,
		       remote_commit_id_before, local_commit_id_before, commit_id,
		       status, created_at, completed_at
		FROM workspace_publications
		WHERE run_id = ? AND idempotency_key = ?`, runID, idempotencyKey))
}

func (store *WorkspacePublicationStore) ActivePublication(
	ctx context.Context,
	workspaceID string,
) (workspace.Publication, error) {
	return scanPublication(store.db.QueryRowContext(ctx, `
		SELECT id, run_id, workspace_id, idempotency_key, commit_message,
		       remote_commit_id_before, local_commit_id_before, commit_id,
		       status, created_at, completed_at
		FROM workspace_publications
		WHERE workspace_id = ? AND status = 'prepared'`, workspaceID))
}

func (store *WorkspacePublicationStore) ReservePublication(
	ctx context.Context,
	candidate workspace.Publication,
) (workspace.Publication, bool, error) {
	if err := candidate.Validate(); err != nil {
		return workspace.Publication{}, false, err
	}
	if candidate.Status != workspace.PublicationStatusPrepared {
		return workspace.Publication{}, false, workspace.ErrPublicationConflict
	}
	result, err := store.db.ExecContext(ctx, `
		INSERT INTO workspace_publications (
			id, run_id, workspace_id, idempotency_key, commit_message,
			remote_commit_id_before, local_commit_id_before, commit_id,
			status, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(run_id, idempotency_key) DO NOTHING`,
		candidate.ID, candidate.RunID, candidate.WorkspaceID,
		candidate.IdempotencyKey, candidate.CommitMessage,
		candidate.RemoteCommitIDBefore, candidate.LocalCommitIDBefore,
		candidate.CommitID, candidate.Status, formatWorkspaceTime(candidate.CreatedAt),
	)
	if err != nil {
		if _, activeErr := store.ActivePublication(ctx, candidate.WorkspaceID); activeErr == nil {
			return workspace.Publication{}, false, workspace.ErrPublicationConflict
		}
		return workspace.Publication{}, false, fmt.Errorf("reserve workspace publication: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workspace.Publication{}, false, fmt.Errorf("read publication row count: %w", err)
	}
	if rows == 1 {
		return candidate, true, nil
	}
	existing, err := store.GetPublication(ctx, candidate.RunID, candidate.IdempotencyKey)
	if err != nil {
		return workspace.Publication{}, false, err
	}
	if !samePublicationRequest(existing, candidate) {
		return workspace.Publication{}, false, workspace.ErrPublicationConflict
	}
	return existing, false, nil
}

func (store *WorkspacePublicationStore) CompletePublication(
	ctx context.Context,
	publicationID string,
	completedAt time.Time,
) (workspace.Publication, error) {
	result, err := store.db.ExecContext(ctx, `
		UPDATE workspace_publications
		SET status = 'completed', completed_at = ?
		WHERE id = ? AND status = 'prepared'`,
		formatWorkspaceTime(completedAt), publicationID,
	)
	if err != nil {
		return workspace.Publication{}, fmt.Errorf("complete workspace publication: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workspace.Publication{}, fmt.Errorf("read publication completion row count: %w", err)
	}
	stored, err := scanPublication(store.db.QueryRowContext(ctx, `
		SELECT id, run_id, workspace_id, idempotency_key, commit_message,
		       remote_commit_id_before, local_commit_id_before, commit_id,
		       status, created_at, completed_at
		FROM workspace_publications WHERE id = ?`, publicationID))
	if err != nil {
		return workspace.Publication{}, err
	}
	if rows == 0 && stored.Status != workspace.PublicationStatusCompleted {
		return workspace.Publication{}, workspace.ErrPublicationConflict
	}
	return stored, nil
}

type publicationScanner interface {
	Scan(...any) error
}

func scanPublication(row publicationScanner) (workspace.Publication, error) {
	var stored workspace.Publication
	var status string
	var createdAt string
	var completedAt sql.NullString
	err := row.Scan(
		&stored.ID, &stored.RunID, &stored.WorkspaceID, &stored.IdempotencyKey,
		&stored.CommitMessage, &stored.RemoteCommitIDBefore,
		&stored.LocalCommitIDBefore, &stored.CommitID, &status, &createdAt,
		&completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.Publication{}, workspace.ErrPublicationNotFound
	}
	if err != nil {
		return workspace.Publication{}, fmt.Errorf("scan workspace publication: %w", err)
	}
	stored.Status = workspace.PublicationStatus(status)
	stored.CreatedAt, err = parsePublicationTime(createdAt)
	if err != nil {
		return workspace.Publication{}, err
	}
	if completedAt.Valid {
		value, parseErr := parsePublicationTime(completedAt.String)
		if parseErr != nil {
			return workspace.Publication{}, parseErr
		}
		stored.CompletedAt = &value
	}
	if err := stored.Validate(); err != nil {
		return workspace.Publication{}, fmt.Errorf("stored workspace publication is invalid: %w", err)
	}
	return stored, nil
}

func parsePublicationTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse workspace publication time: %w", err)
	}
	return parsed.UTC(), nil
}

func samePublicationRequest(left, right workspace.Publication) bool {
	return left.ID == right.ID && left.RunID == right.RunID &&
		left.WorkspaceID == right.WorkspaceID &&
		left.IdempotencyKey == right.IdempotencyKey &&
		left.CommitMessage == right.CommitMessage
}
