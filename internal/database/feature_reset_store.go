package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

var _ workorder.ResetStore = (*FeatureDeletionStore)(nil)

// BeginReturnToReady loads a started, unmerged work order and its workspace
// once no agent is working on it.
func (store *FeatureDeletionStore) BeginReturnToReady(
	ctx context.Context,
	projectID string,
	featureID string,
) (workorder.Deletion, error) {
	storedFeature, err := scanFeature(store.db.QueryRowContext(ctx, `
		SELECT id, project_id, title, description, state, accepted_goal,
		       goal_accepted_at,
		       planning_round_limit, implementation_review_round_limit,
		       lead_provider, reviewer_provider, lead_agent, reviewer_agent, lead_model, reviewer_model,
		       merge_policy, autonomy_policy, independent_acceptance_tests, skip_plan, skip_reviewer,
		       created_at, updated_at
		FROM features WHERE id = ?`, featureID))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && storedFeature.ProjectID != projectID) {
		return workorder.Deletion{}, feature.ErrNotFound
	}
	if err != nil {
		return workorder.Deletion{}, fmt.Errorf("load work order to return to Ready: %w", err)
	}
	switch storedFeature.State {
	case feature.StatePlanning, feature.StateImplementing, feature.StateReviewing,
		feature.StateReadyToMerge, feature.StateReady:
	default:
		return workorder.Deletion{}, workorder.ErrNotStarted
	}
	var active int
	if err := store.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM runs r
			WHERE r.feature_id = ? AND (
				r.status IN (?, ?) OR EXISTS (
					SELECT 1 FROM sessions s
					WHERE s.run_id = r.id AND s.status NOT IN (?, ?, ?)
				)
			)
		)`,
		featureID, execution.RunStatusRunning, execution.RunStatusWaitingForUser,
		execution.SessionStatusCompleted, execution.SessionStatusStopped, execution.SessionStatusFailed,
	).Scan(&active); err != nil {
		return workorder.Deletion{}, fmt.Errorf("check active work-order agents: %w", err)
	}
	if active != 0 {
		return workorder.Deletion{}, workorder.ErrActive
	}
	claimed := workorder.Deletion{Feature: storedFeature}
	storedWorkspace, err := scanWorkspace(store.db.QueryRowContext(ctx, `
		SELECT id, project_id, feature_id, repository_owner, repository_name,
		       base_branch, branch_name, base_commit_id, status,
		       branch_created_at, checkout_relative_path, checkout_created_at,
		       pull_request_number, pull_request_url, pull_request_recorded_at,
		       approved_commit_id, merge_ready_at, merge_commit_id, merged_at,
		       created_at, updated_at
		FROM feature_workspaces WHERE feature_id = ?`, featureID))
	if err == nil {
		claimed.Workspace = &storedWorkspace
	} else if !errors.Is(err, sql.ErrNoRows) {
		return workorder.Deletion{}, fmt.Errorf("load managed workspace to return to Ready: %w", err)
	}
	return claimed, nil
}

// FinishReturnToReady forgets the work order's workspace and goal acceptance.
// Runs and their conversations stay in the history.
func (store *FeatureDeletionStore) FinishReturnToReady(
	ctx context.Context,
	projectID string,
	featureID string,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin return to Ready: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`DELETE FROM feature_workspaces WHERE feature_id = ?`,
		`UPDATE features SET accepted_goal = '', goal_accepted_at = NULL WHERE id = ?`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, featureID); err != nil {
			return fmt.Errorf("return work order %q to Ready: %w", featureID, err)
		}
	}
	var storedProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM features WHERE id = ?`, featureID).Scan(&storedProjectID); err != nil || storedProjectID != projectID {
		return feature.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit return to Ready: %w", err)
	}
	return nil
}
