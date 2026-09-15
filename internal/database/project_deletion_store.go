package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
)

type ProjectDeletionStore struct {
	db  *sql.DB
	now func() time.Time
}

var _ projectdeletion.Store = (*ProjectDeletionStore)(nil)

func NewProjectDeletionStore(db *sql.DB) *ProjectDeletionStore {
	return &ProjectDeletionStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (store *ProjectDeletionStore) BeginDeletion(
	ctx context.Context,
	projectID string,
	idempotencyKey string,
	force bool,
) (projectdeletion.Deletion, error) {
	projectID, idempotencyKey = strings.TrimSpace(projectID), strings.TrimSpace(idempotencyKey)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return projectdeletion.Deletion{}, fmt.Errorf("begin project deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanProjectDeletion(tx.QueryRowContext(ctx, `
		SELECT project_id, idempotency_key, force, repository_owner,
		       repository_name, repository_default_branch, repository_bound_at, status
		FROM project_deletions
		WHERE project_id = ? OR idempotency_key = ?`, projectID, idempotencyKey))
	if err == nil {
		if existing.Completed && existing.ProjectID == projectID && existing.IdempotencyKey != idempotencyKey {
			return projectdeletion.Deletion{}, project.ErrNotFound
		}
		if existing.ProjectID != projectID || existing.IdempotencyKey != idempotencyKey || existing.Force != force {
			return projectdeletion.Deletion{}, projectdeletion.ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return projectdeletion.Deletion{}, fmt.Errorf("load project deletion: %w", err)
	}

	storedProject, err := scanProject(tx.QueryRowContext(ctx, `
		SELECT id, name, recovery_policy, merge_policy, autonomy_policy,
		       planning_round_limit, implementation_review_round_limit,
		       lead_provider, reviewer_provider, lead_model, reviewer_model,
		       forgejo_owner, forgejo_repository, forgejo_default_branch, forgejo_bound_at,
		       created_at
		FROM projects WHERE id = ?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return projectdeletion.Deletion{}, project.ErrNotFound
	}
	if err != nil {
		return projectdeletion.Deletion{}, fmt.Errorf("load project for deletion: %w", err)
	}
	if !force {
		var active int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM features f
				JOIN runs r ON r.feature_id = f.id
				WHERE f.project_id = ? AND (
					r.status = ? OR EXISTS (
						SELECT 1 FROM sessions s
						WHERE s.run_id = r.id
						  AND s.status NOT IN (?, ?, ?, ?)
					)
				)
			)`, projectID, execution.RunStatusRunning,
			execution.SessionStatusWaitingForUser, execution.SessionStatusCompleted,
			execution.SessionStatusStopped, execution.SessionStatusFailed).Scan(&active); err != nil {
			return projectdeletion.Deletion{}, fmt.Errorf("check active project runs: %w", err)
		}
		if active != 0 {
			return projectdeletion.Deletion{}, projectdeletion.ErrActive
		}
	}

	deletion := projectdeletion.Deletion{
		ProjectID: projectID, IdempotencyKey: idempotencyKey, Force: force,
	}
	var owner, repository, branch string
	var boundAt any
	if storedProject.ForgejoRepository != nil {
		copy := *storedProject.ForgejoRepository
		deletion.Repository = &copy
		owner, repository, branch = copy.Owner, copy.Name, copy.DefaultBranch
		boundAt = copy.BoundAt.UTC().Format(time.RFC3339Nano)
		var shared int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM projects
				WHERE id != ? AND lower(forgejo_owner) = lower(?)
				  AND lower(forgejo_repository) = lower(?)
			)`, projectID, owner, repository).Scan(&shared); err != nil {
			return projectdeletion.Deletion{}, fmt.Errorf("check repository ownership: %w", err)
		}
		if shared != 0 {
			return projectdeletion.Deletion{}, projectdeletion.ErrUnsafeArtifacts
		}
	}
	var unsafeWorkspace int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM feature_workspaces w
			JOIN features f ON f.id = w.feature_id
			WHERE f.project_id = ? AND (
				? = '' OR lower(w.repository_owner) != lower(?)
				OR lower(w.repository_name) != lower(?)
			)
		)`, projectID, repository, owner, repository).Scan(&unsafeWorkspace); err != nil {
		return projectdeletion.Deletion{}, fmt.Errorf("check work-order repository ownership: %w", err)
	}
	if unsafeWorkspace != 0 {
		return projectdeletion.Deletion{}, projectdeletion.ErrUnsafeArtifacts
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_deletions (
			project_id, idempotency_key, force, repository_owner, repository_name,
			repository_default_branch, repository_bound_at, status, requested_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		projectID, idempotencyKey, force, owner, repository, branch, boundAt,
		formatExecutionTime(store.now())); err != nil {
		return projectdeletion.Deletion{}, fmt.Errorf("claim project deletion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return projectdeletion.Deletion{}, fmt.Errorf("commit project deletion claim: %w", err)
	}
	return deletion, nil
}

func (store *ProjectDeletionStore) ReleaseDeletion(ctx context.Context, projectID, idempotencyKey string) error {
	_, err := store.db.ExecContext(ctx, `
		DELETE FROM project_deletions
		WHERE project_id = ? AND idempotency_key = ? AND status = 'pending'`, projectID, idempotencyKey)
	if err != nil {
		return fmt.Errorf("release project deletion: %w", err)
	}
	return nil
}

func (store *ProjectDeletionStore) ListFeatureIDs(ctx context.Context, projectID string) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id FROM features WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project work orders: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan project work order: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project work orders: %w", err)
	}
	return ids, nil
}

func (store *ProjectDeletionStore) FinishDeletion(ctx context.Context, projectID, idempotencyKey string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin final project cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	deletion, err := scanProjectDeletion(tx.QueryRowContext(ctx, `
		SELECT project_id, idempotency_key, force, repository_owner,
		       repository_name, repository_default_branch, repository_bound_at, status
		FROM project_deletions WHERE project_id = ?`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return project.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load final project deletion: %w", err)
	}
	if deletion.IdempotencyKey != idempotencyKey {
		return projectdeletion.ErrConflict
	}
	if deletion.Completed {
		return nil
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM features WHERE project_id = ?`, projectID).Scan(&remaining); err != nil {
		return fmt.Errorf("count remaining work orders: %w", err)
	}
	if remaining != 0 {
		return errors.New("project still owns work orders")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("delete project row: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("delete project row: affected=%d err=%v", affected, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE project_deletions SET status = 'completed', completed_at = ?
		WHERE project_id = ? AND idempotency_key = ? AND status = 'pending'`,
		formatExecutionTime(store.now()), projectID, idempotencyKey); err != nil {
		return fmt.Errorf("complete project tombstone: %w", err)
	}
	return tx.Commit()
}

type rowScanner interface {
	Scan(...any) error
}

func scanProjectDeletion(row rowScanner) (projectdeletion.Deletion, error) {
	var deletion projectdeletion.Deletion
	var force int
	var owner, repository, branch, status string
	var boundAt sql.NullString
	if err := row.Scan(&deletion.ProjectID, &deletion.IdempotencyKey, &force, &owner,
		&repository, &branch, &boundAt, &status); err != nil {
		return projectdeletion.Deletion{}, err
	}
	deletion.Force = force != 0
	deletion.Completed = status == "completed"
	if repository != "" {
		parsed, err := parseExecutionTime(boundAt.String)
		if err != nil || !boundAt.Valid {
			return projectdeletion.Deletion{}, errors.New("project deletion repository binding is invalid")
		}
		deletion.Repository = &project.ForgejoRepository{
			Owner: owner, Name: repository, DefaultBranch: branch, BoundAt: parsed,
		}
	}
	return deletion, nil
}
