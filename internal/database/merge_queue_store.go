package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// JoinMergeQueue adds the run at the back of its project's merge queue; a run
// already in line keeps its place.
func (s *ExecutionStore) JoinMergeQueue(ctx context.Context, projectID, runID string) error {
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO merge_queue (run_id, project_id, joined_at) VALUES (?, ?, ?)
		 ON CONFLICT (run_id) DO NOTHING`,
		runID, projectID, formatExecutionTime(time.Now().UTC()),
	); err != nil {
		return fmt.Errorf("add run %q to the merge queue: %w", runID, err)
	}
	return nil
}

// LeaveMergeQueue takes the run out of line, reporting its project when it
// was in one.
func (s *ExecutionStore) LeaveMergeQueue(ctx context.Context, runID string) (string, bool, error) {
	var projectID string
	err := s.db.QueryRowContext(
		ctx, `DELETE FROM merge_queue WHERE run_id = ? RETURNING project_id`, runID,
	).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("take run %q out of the merge queue: %w", runID, err)
	}
	return projectID, true, nil
}

// MergeQueue lists a project's runs in line, first first.
func (s *ExecutionStore) MergeQueue(ctx context.Context, projectID string) ([]string, error) {
	return s.queryMergeQueue(ctx,
		`SELECT run_id FROM merge_queue WHERE project_id = ? ORDER BY joined_at, rowid`, projectID)
}

// MergeQueueProjects lists the projects with a run in line.
func (s *ExecutionStore) MergeQueueProjects(ctx context.Context) ([]string, error) {
	return s.queryMergeQueue(ctx, `SELECT DISTINCT project_id FROM merge_queue ORDER BY project_id`)
}

func (s *ExecutionStore) queryMergeQueue(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list the merge queue: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan the merge queue: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
