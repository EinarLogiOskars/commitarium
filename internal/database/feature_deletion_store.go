package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

type FeatureDeletionStore struct {
	db  *sql.DB
	now func() time.Time
}

var _ workorder.Store = (*FeatureDeletionStore)(nil)

func NewFeatureDeletionStore(db *sql.DB) *FeatureDeletionStore {
	return &FeatureDeletionStore{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (store *FeatureDeletionStore) BeginDeletion(
	ctx context.Context,
	projectID string,
	featureID string,
) (workorder.Deletion, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workorder.Deletion{}, fmt.Errorf("begin work-order deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	storedFeature, err := scanFeature(tx.QueryRowContext(ctx, `
		SELECT id, project_id, title, description, state, accepted_goal,
		       goal_accepted_at, created_at, updated_at
		FROM features WHERE id = ?`, featureID))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && storedFeature.ProjectID != projectID) {
		return workorder.Deletion{}, feature.ErrNotFound
	}
	if err != nil {
		return workorder.Deletion{}, fmt.Errorf("load work order for deletion: %w", err)
	}

	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM runs r
			WHERE r.feature_id = ? AND (
				r.status = ? OR EXISTS (
					SELECT 1 FROM sessions s
					WHERE s.run_id = r.id
					  AND s.status NOT IN (?, ?, ?, ?)
				)
			)
		)`,
		featureID, execution.RunStatusRunning,
		execution.SessionStatusWaitingForUser, execution.SessionStatusCompleted,
		execution.SessionStatusStopped, execution.SessionStatusFailed,
	).Scan(&active); err != nil {
		return workorder.Deletion{}, fmt.Errorf("check active work-order agents: %w", err)
	}
	if active != 0 {
		return workorder.Deletion{}, workorder.ErrActive
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO feature_deletions (feature_id, requested_at)
		VALUES (?, ?)
		ON CONFLICT(feature_id) DO NOTHING`,
		featureID, formatExecutionTime(store.now()),
	); err != nil {
		return workorder.Deletion{}, fmt.Errorf("claim work-order deletion: %w", err)
	}

	deletion := workorder.Deletion{Feature: storedFeature}
	storedWorkspace, err := scanWorkspace(tx.QueryRowContext(ctx, `
		SELECT id, project_id, feature_id, repository_owner, repository_name,
		       base_branch, branch_name, base_commit_id, status,
		       branch_created_at, checkout_relative_path, checkout_created_at,
		       pull_request_number, pull_request_url, pull_request_recorded_at,
		       approved_commit_id, merge_ready_at, merge_commit_id, merged_at,
		       created_at, updated_at
		FROM feature_workspaces WHERE feature_id = ?`, featureID))
	if err == nil {
		deletion.Workspace = &storedWorkspace
	} else if !errors.Is(err, sql.ErrNoRows) {
		return workorder.Deletion{}, fmt.Errorf("load managed workspace for deletion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return workorder.Deletion{}, fmt.Errorf("commit work-order deletion claim: %w", err)
	}
	return deletion, nil
}

func (store *FeatureDeletionStore) FinishDeletion(
	ctx context.Context,
	projectID string,
	featureID string,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin internal work-order cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var storedProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM features WHERE id = ?`, featureID).Scan(&storedProjectID); errors.Is(err, sql.ErrNoRows) {
		return feature.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("verify work order for cleanup: %w", err)
	}
	if storedProjectID != projectID {
		return feature.ErrNotFound
	}
	var claimed int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM feature_deletions WHERE feature_id = ?)`, featureID).Scan(&claimed); err != nil {
		return fmt.Errorf("verify work-order deletion claim: %w", err)
	}
	if claimed == 0 {
		return errors.New("work-order deletion was not claimed")
	}

	statements := []string{
		`DELETE FROM run_plan_revisions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?)`,
		`DELETE FROM planning_messages WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?)`,
		`DELETE FROM run_interventions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?)`,
		`DELETE FROM run_actions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?)`,
		`DELETE FROM session_commands WHERE session_id IN (SELECT id FROM sessions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?))`,
		`DELETE FROM worker_attempt_checkpoints WHERE session_id IN (SELECT id FROM sessions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?))`,
		`DELETE FROM session_events WHERE session_id IN (SELECT id FROM sessions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?))`,
		`DELETE FROM sessions WHERE run_id IN (SELECT id FROM runs WHERE feature_id = ?)`,
		`DELETE FROM runs WHERE feature_id = ?`,
		`DELETE FROM workflow_events WHERE aggregate_id = ?`,
		`DELETE FROM feature_workspaces WHERE feature_id = ?`,
		`DELETE FROM feature_deletions WHERE feature_id = ?`,
		`DELETE FROM features WHERE id = ? AND project_id = ?`,
	}
	for index, statement := range statements {
		arguments := []any{featureID}
		if index == len(statements)-1 {
			arguments = []any{featureID, projectID}
		}
		if _, err := tx.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("delete internal work-order artifacts at step %d: %w", index+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit internal work-order cleanup: %w", err)
	}
	return nil
}
