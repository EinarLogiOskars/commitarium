package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type WorkspaceStore struct {
	db *sql.DB
}

var _ workspace.Store = (*WorkspaceStore)(nil)

func NewWorkspaceStore(db *sql.DB) *WorkspaceStore {
	return &WorkspaceStore{db: db}
}

func (store *WorkspaceStore) GetByFeatureID(
	ctx context.Context,
	featureID string,
) (workspace.Workspace, error) {
	stored, err := scanWorkspace(store.db.QueryRowContext(
		ctx,
		`SELECT id, project_id, feature_id, repository_owner, repository_name,
		        base_branch, branch_name, base_commit_id, status,
		        branch_created_at, created_at, updated_at
		 FROM feature_workspaces WHERE feature_id = ?`,
		featureID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.Workspace{}, workspace.ErrNotFound
	}
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("select workspace for feature %q: %w", featureID, err)
	}
	return stored, nil
}

func (store *WorkspaceStore) Reserve(
	ctx context.Context,
	reservation workspace.Workspace,
) (workspace.Workspace, bool, error) {
	if err := reservation.Validate(); err != nil {
		return workspace.Workspace{}, false, err
	}
	if reservation.Status != workspace.StatusPreparing {
		return workspace.Workspace{}, false, errors.New("workspace reservation must be preparing")
	}
	result, err := store.db.ExecContext(
		ctx,
		`INSERT INTO feature_workspaces (
		    id, project_id, feature_id, repository_owner, repository_name,
		    base_branch, branch_name, base_commit_id, status,
		    branch_created_at, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
		 ON CONFLICT(feature_id) DO NOTHING`,
		reservation.ID, reservation.ProjectID, reservation.FeatureID,
		reservation.RepositoryOwner, reservation.RepositoryName,
		reservation.BaseBranch, reservation.Branch, reservation.BaseCommitID,
		reservation.Status, formatWorkspaceTime(reservation.CreatedAt),
		formatWorkspaceTime(reservation.UpdatedAt),
	)
	if err != nil {
		return workspace.Workspace{}, false, fmt.Errorf("reserve workspace %q: %w", reservation.ID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return workspace.Workspace{}, false, fmt.Errorf("read workspace reservation row count: %w", err)
	}
	if rowsAffected == 1 {
		return reservation, true, nil
	}
	if rowsAffected != 0 {
		return workspace.Workspace{}, false, fmt.Errorf("reserve workspace %q: expected zero or one affected row, got %d", reservation.ID, rowsAffected)
	}
	existing, err := store.GetByFeatureID(ctx, reservation.FeatureID)
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	if !sameWorkspaceReservation(existing, reservation) {
		return workspace.Workspace{}, false, workspace.ErrConflict
	}
	return existing, false, nil
}

func (store *WorkspaceStore) MarkBranchReady(
	ctx context.Context,
	featureID string,
	readyAt time.Time,
) (workspace.Workspace, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("begin branch-ready update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := scanWorkspace(tx.QueryRowContext(
		ctx,
		`SELECT id, project_id, feature_id, repository_owner, repository_name,
		        base_branch, branch_name, base_commit_id, status,
		        branch_created_at, created_at, updated_at
		 FROM feature_workspaces WHERE feature_id = ?`,
		featureID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.Workspace{}, workspace.ErrNotFound
	}
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("select workspace for branch-ready update: %w", err)
	}
	if stored.Status == workspace.StatusBranchReady {
		return stored, nil
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE feature_workspaces
		 SET status = ?, branch_created_at = ?, updated_at = ?
		 WHERE feature_id = ? AND status = ?`,
		workspace.StatusBranchReady, formatWorkspaceTime(readyAt),
		formatWorkspaceTime(readyAt), featureID, workspace.StatusPreparing,
	)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("mark workspace branch ready: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("read branch-ready update row count: %w", err)
	}
	if rowsAffected != 1 {
		return workspace.Workspace{}, fmt.Errorf(
			"mark workspace branch ready: expected one affected row, got %d",
			rowsAffected,
		)
	}
	if err := tx.Commit(); err != nil {
		return workspace.Workspace{}, fmt.Errorf("commit branch-ready update: %w", err)
	}
	readyAt = readyAt.UTC()
	stored.Status = workspace.StatusBranchReady
	stored.BranchCreatedAt = &readyAt
	stored.UpdatedAt = readyAt
	return stored, nil
}

type workspaceScanner interface {
	Scan(dest ...any) error
}

func scanWorkspace(scanner workspaceScanner) (workspace.Workspace, error) {
	stored := workspace.Workspace{}
	var status string
	var branchCreatedAt sql.NullString
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(
		&stored.ID, &stored.ProjectID, &stored.FeatureID,
		&stored.RepositoryOwner, &stored.RepositoryName,
		&stored.BaseBranch, &stored.Branch, &stored.BaseCommitID,
		&status, &branchCreatedAt, &createdAt, &updatedAt,
	); err != nil {
		return workspace.Workspace{}, err
	}
	stored.Status = workspace.Status(status)
	var err error
	stored.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("parse workspace creation time: %w", err)
	}
	stored.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("parse workspace update time: %w", err)
	}
	if branchCreatedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, branchCreatedAt.String)
		if err != nil {
			return workspace.Workspace{}, fmt.Errorf("parse branch creation time: %w", err)
		}
		stored.BranchCreatedAt = &parsed
	}
	if err := stored.Validate(); err != nil {
		return workspace.Workspace{}, fmt.Errorf("validate stored workspace: %w", err)
	}
	return stored, nil
}

func sameWorkspaceReservation(left, right workspace.Workspace) bool {
	return left.ID == right.ID && left.ProjectID == right.ProjectID &&
		left.FeatureID == right.FeatureID &&
		left.RepositoryOwner == right.RepositoryOwner &&
		left.RepositoryName == right.RepositoryName &&
		left.BaseBranch == right.BaseBranch && left.Branch == right.Branch &&
		left.BaseCommitID == right.BaseCommitID
}

func formatWorkspaceTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
