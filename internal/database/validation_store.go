package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/validation"
)

type ValidationStore struct{ db *sql.DB }

func NewValidationStore(db *sql.DB) *ValidationStore { return &ValidationStore{db: db} }

func (store *ValidationStore) PutConfig(ctx context.Context, config validation.Config) (validation.Config, error) {
	commands, _ := json.Marshal(config.Commands)
	_, err := store.db.ExecContext(ctx, `INSERT INTO project_validation_configs (project_id, commands_json, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(project_id) DO UPDATE SET commands_json = excluded.commands_json, updated_at = excluded.updated_at`,
		config.ProjectID, string(commands), formatExecutionTime(config.UpdatedAt))
	if err != nil {
		return validation.Config{}, fmt.Errorf("store validation config: %w", err)
	}
	return store.GetConfig(ctx, config.ProjectID)
}

func (store *ValidationStore) GetConfig(ctx context.Context, projectID string) (validation.Config, error) {
	var config validation.Config
	var commandsJSON, updatedAt string
	err := store.db.QueryRowContext(ctx, `SELECT project_id, commands_json, updated_at FROM project_validation_configs WHERE project_id = ?`, projectID).
		Scan(&config.ProjectID, &commandsJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return validation.Config{}, validation.ErrNotFound
	}
	if err != nil {
		return validation.Config{}, err
	}
	if err := json.Unmarshal([]byte(commandsJSON), &config.Commands); err != nil {
		return validation.Config{}, err
	}
	config.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return validation.Config{}, err
	}
	return config, config.Validate()
}

func (store *ValidationStore) CreateJob(ctx context.Context, job validation.Job) (validation.Job, bool, error) {
	commands, _ := json.Marshal(job.Commands)
	results, _ := json.Marshal(job.Results)
	result, err := store.db.ExecContext(ctx, `INSERT INTO validation_jobs (
		id, project_id, feature_id, run_id, workspace_id, commit_id, commands_json,
		status, results_json, error, created_at, updated_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL) ON CONFLICT(id) DO NOTHING`,
		job.ID, job.ProjectID, job.FeatureID, job.RunID, job.WorkspaceID, job.CommitID,
		string(commands), job.Status, string(results), job.Error,
		formatExecutionTime(job.CreatedAt), formatExecutionTime(job.UpdatedAt))
	if err != nil {
		return validation.Job{}, false, fmt.Errorf("create validation job: %w", err)
	}
	count, _ := result.RowsAffected()
	stored, err := store.GetJob(ctx, job.ID)
	if err != nil {
		return validation.Job{}, false, err
	}
	if count == 0 && (stored.RunID != job.RunID || stored.CommitID != job.CommitID ||
		stored.WorkspaceID != job.WorkspaceID || !slices.Equal(stored.Commands, job.Commands)) {
		return validation.Job{}, false, validation.ErrConflict
	}
	return stored, count == 1, nil
}

func (store *ValidationStore) GetJob(ctx context.Context, id string) (validation.Job, error) {
	job, err := scanValidationJob(store.db.QueryRowContext(ctx, validationJobSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return validation.Job{}, validation.ErrNotFound
	}
	return job, err
}

func (store *ValidationStore) ListJobsByRun(ctx context.Context, runID string) ([]validation.Job, error) {
	rows, err := store.db.QueryContext(ctx, validationJobSelect+` WHERE run_id = ? ORDER BY created_at DESC, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]validation.Job, 0)
	for rows.Next() {
		job, err := scanValidationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (store *ValidationStore) TransitionJob(ctx context.Context, id string, from, to validation.Status, results []validation.CommandResult, detail string, occurredAt time.Time) (validation.Job, bool, error) {
	current, err := store.GetJob(ctx, id)
	if err != nil {
		return validation.Job{}, false, err
	}
	if current.Status == to {
		return current, false, nil
	}
	if current.Status != from {
		return validation.Job{}, false, validation.ErrConflict
	}
	resultsJSON, _ := json.Marshal(results)
	var completed any
	if to == validation.StatusPassed || to == validation.StatusFailed {
		completed = formatExecutionTime(occurredAt)
	}
	result, err := store.db.ExecContext(ctx, `UPDATE validation_jobs SET status = ?, results_json = ?, error = ?, updated_at = ?, completed_at = ? WHERE id = ? AND status = ?`,
		to, string(resultsJSON), detail, formatExecutionTime(occurredAt), completed, id, from)
	if err != nil {
		return validation.Job{}, false, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return validation.Job{}, false, validation.ErrConflict
	}
	updated, err := store.GetJob(ctx, id)
	return updated, true, err
}

const validationJobSelect = `SELECT id, project_id, feature_id, run_id, workspace_id, commit_id,
	commands_json, status, results_json, error, created_at, updated_at, completed_at FROM validation_jobs`

type validationScanner interface{ Scan(...any) error }

func scanValidationJob(scanner validationScanner) (validation.Job, error) {
	var job validation.Job
	var commandsJSON, resultsJSON, createdAt, updatedAt string
	var completedAt sql.NullString
	if err := scanner.Scan(&job.ID, &job.ProjectID, &job.FeatureID, &job.RunID, &job.WorkspaceID,
		&job.CommitID, &commandsJSON, &job.Status, &resultsJSON, &job.Error, &createdAt, &updatedAt, &completedAt); err != nil {
		return validation.Job{}, err
	}
	if err := json.Unmarshal([]byte(commandsJSON), &job.Commands); err != nil {
		return validation.Job{}, err
	}
	if err := json.Unmarshal([]byte(resultsJSON), &job.Results); err != nil {
		return validation.Job{}, err
	}
	var err error
	job.CreatedAt, err = parseExecutionTime(createdAt)
	if err != nil {
		return validation.Job{}, err
	}
	job.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return validation.Job{}, err
	}
	if completedAt.Valid {
		value, err := parseExecutionTime(completedAt.String)
		if err != nil {
			return validation.Job{}, err
		}
		job.CompletedAt = &value
	}
	if err := job.Validate(); err != nil {
		return validation.Job{}, err
	}
	return job, nil
}
