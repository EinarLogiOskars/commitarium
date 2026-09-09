package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
		        branch_created_at, checkout_relative_path, checkout_created_at,
		        pull_request_number, pull_request_url, pull_request_recorded_at,
		        created_at, updated_at
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
		    branch_created_at, checkout_relative_path, checkout_created_at,
		    pull_request_number, pull_request_url, pull_request_recorded_at,
		    created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '', NULL, NULL, '', NULL, ?, ?)
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
		        branch_created_at, checkout_relative_path, checkout_created_at,
		        pull_request_number, pull_request_url, pull_request_recorded_at,
		        created_at, updated_at
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

func (store *WorkspaceStore) MarkCheckoutReady(
	ctx context.Context,
	featureID string,
	relativePath string,
	readyAt time.Time,
) (workspace.Workspace, error) {
	if relativePath == "" || relativePath != strings.TrimSpace(relativePath) {
		return workspace.Workspace{}, errors.New("checkout relative path is required and must be trimmed")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("begin checkout-ready update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := scanWorkspace(tx.QueryRowContext(
		ctx,
		`SELECT id, project_id, feature_id, repository_owner, repository_name,
		        base_branch, branch_name, base_commit_id, status,
		        branch_created_at, checkout_relative_path, checkout_created_at,
		        pull_request_number, pull_request_url, pull_request_recorded_at,
		        created_at, updated_at
		 FROM feature_workspaces WHERE feature_id = ?`,
		featureID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.Workspace{}, workspace.ErrNotFound
	}
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("select workspace for checkout-ready update: %w", err)
	}
	if stored.CheckoutReady() {
		if stored.CheckoutRelativePath != relativePath {
			return workspace.Workspace{}, workspace.ErrConflict
		}
		return stored, nil
	}
	if stored.Status != workspace.StatusBranchReady {
		return workspace.Workspace{}, workspace.ErrConflict
	}
	if readyAt.IsZero() || readyAt.Before(*stored.BranchCreatedAt) {
		return workspace.Workspace{}, errors.New("checkout readiness cannot precede branch readiness")
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE feature_workspaces
		 SET checkout_relative_path = ?, checkout_created_at = ?, updated_at = ?
		 WHERE feature_id = ? AND status = ?
		   AND checkout_relative_path = '' AND checkout_created_at IS NULL`,
		relativePath, formatWorkspaceTime(readyAt), formatWorkspaceTime(readyAt),
		featureID, workspace.StatusBranchReady,
	)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("mark workspace checkout ready: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("read checkout-ready update row count: %w", err)
	}
	if rowsAffected != 1 {
		return workspace.Workspace{}, workspace.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return workspace.Workspace{}, fmt.Errorf("commit checkout-ready update: %w", err)
	}
	readyAt = readyAt.UTC()
	stored.CheckoutRelativePath = relativePath
	stored.CheckoutCreatedAt = &readyAt
	stored.UpdatedAt = readyAt
	return stored, nil
}

func (store *WorkspaceStore) MarkPullRequestReady(
	ctx context.Context,
	featureID string,
	number int64,
	pullRequestURL string,
	recordedAt time.Time,
) (workspace.Workspace, error) {
	if number < 1 || strings.TrimSpace(pullRequestURL) == "" ||
		pullRequestURL != strings.TrimSpace(pullRequestURL) {
		return workspace.Workspace{}, errors.New("pull request identity is invalid")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("begin pull-request-ready update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := scanWorkspace(tx.QueryRowContext(
		ctx,
		`SELECT id, project_id, feature_id, repository_owner, repository_name,
		        base_branch, branch_name, base_commit_id, status,
		        branch_created_at, checkout_relative_path, checkout_created_at,
		        pull_request_number, pull_request_url, pull_request_recorded_at,
		        created_at, updated_at
		 FROM feature_workspaces WHERE feature_id = ?`,
		featureID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.Workspace{}, workspace.ErrNotFound
	}
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("select workspace for pull-request-ready update: %w", err)
	}
	if stored.PullRequestReady() {
		if stored.PullRequestNumber != number || stored.PullRequestURL != pullRequestURL {
			return workspace.Workspace{}, workspace.ErrConflict
		}
		return stored, nil
	}
	if !stored.CheckoutReady() || recordedAt.IsZero() ||
		recordedAt.Before(*stored.CheckoutCreatedAt) {
		return workspace.Workspace{}, workspace.ErrConflict
	}
	recordedAt = recordedAt.UTC()
	ready := stored
	ready.PullRequestNumber = number
	ready.PullRequestURL = pullRequestURL
	ready.PullRequestRecordedAt = &recordedAt
	ready.UpdatedAt = recordedAt
	if err := ready.Validate(); err != nil {
		return workspace.Workspace{}, err
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE feature_workspaces
		 SET pull_request_number = ?, pull_request_url = ?,
		     pull_request_recorded_at = ?, updated_at = ?
		 WHERE feature_id = ? AND status = ?
		   AND checkout_relative_path != '' AND checkout_created_at IS NOT NULL
		   AND pull_request_number IS NULL AND pull_request_url = ''
		   AND pull_request_recorded_at IS NULL`,
		number, pullRequestURL, formatWorkspaceTime(recordedAt),
		formatWorkspaceTime(recordedAt), featureID, workspace.StatusBranchReady,
	)
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("mark workspace pull request ready: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("read pull-request-ready update row count: %w", err)
	}
	if rowsAffected != 1 {
		return workspace.Workspace{}, workspace.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return workspace.Workspace{}, fmt.Errorf("commit pull-request-ready update: %w", err)
	}
	return ready, nil
}

type workspaceScanner interface {
	Scan(dest ...any) error
}

func scanWorkspace(scanner workspaceScanner) (workspace.Workspace, error) {
	stored := workspace.Workspace{}
	var status string
	var branchCreatedAt sql.NullString
	var checkoutCreatedAt sql.NullString
	var pullRequestNumber sql.NullInt64
	var pullRequestRecordedAt sql.NullString
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(
		&stored.ID, &stored.ProjectID, &stored.FeatureID,
		&stored.RepositoryOwner, &stored.RepositoryName,
		&stored.BaseBranch, &stored.Branch, &stored.BaseCommitID,
		&status, &branchCreatedAt, &stored.CheckoutRelativePath,
		&checkoutCreatedAt, &pullRequestNumber, &stored.PullRequestURL,
		&pullRequestRecordedAt, &createdAt, &updatedAt,
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
	if checkoutCreatedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, checkoutCreatedAt.String)
		if err != nil {
			return workspace.Workspace{}, fmt.Errorf("parse checkout creation time: %w", err)
		}
		stored.CheckoutCreatedAt = &parsed
	}
	if pullRequestNumber.Valid {
		stored.PullRequestNumber = pullRequestNumber.Int64
	}
	if pullRequestRecordedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, pullRequestRecordedAt.String)
		if err != nil {
			return workspace.Workspace{}, fmt.Errorf("parse pull request recording time: %w", err)
		}
		stored.PullRequestRecordedAt = &parsed
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
