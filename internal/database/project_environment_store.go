package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
)

type ProjectEnvironmentStore struct{ db *sql.DB }

func NewProjectEnvironmentStore(db *sql.DB) *ProjectEnvironmentStore {
	return &ProjectEnvironmentStore{db: db}
}

func (store *ProjectEnvironmentStore) Create(ctx context.Context, request projectenvironment.Request) (projectenvironment.Request, bool, error) {
	packages, _ := json.Marshal(request.SystemPackages)
	resolved, _ := json.Marshal(request.ResolvedPackages)
	result, err := store.db.ExecContext(ctx, `INSERT INTO project_environment_requests (
		id, project_id, feature_id, run_id, session_id, attempt_id, packages_json,
		reason, status, resolved_packages_json, error, requested_at, updated_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	ON CONFLICT(id) DO NOTHING`, request.ID, request.ProjectID, request.FeatureID, request.RunID,
		request.SessionID, request.AttemptID, string(packages), request.Reason, request.Status,
		string(resolved), request.Error, formatExecutionTime(request.RequestedAt), formatExecutionTime(request.UpdatedAt))
	if err != nil {
		return projectenvironment.Request{}, false, fmt.Errorf("insert project environment request: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return projectenvironment.Request{}, false, err
	}
	stored, err := store.Get(ctx, request.ID)
	if err != nil {
		return projectenvironment.Request{}, false, err
	}
	if count == 0 && (stored.ProjectID != request.ProjectID || stored.FeatureID != request.FeatureID ||
		stored.RunID != request.RunID || stored.SessionID != request.SessionID || stored.AttemptID != request.AttemptID ||
		stored.Reason != request.Reason || !slices.Equal(stored.SystemPackages, request.SystemPackages)) {
		return projectenvironment.Request{}, false, projectenvironment.ErrConflict
	}
	return stored, count == 1, nil
}

func (store *ProjectEnvironmentStore) Get(ctx context.Context, id string) (projectenvironment.Request, error) {
	request, err := scanEnvironmentRequest(store.db.QueryRowContext(ctx, environmentSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return projectenvironment.Request{}, projectenvironment.ErrNotFound
	}
	return request, err
}

func (store *ProjectEnvironmentStore) ListByProject(ctx context.Context, projectID string) ([]projectenvironment.Request, error) {
	rows, err := store.db.QueryContext(ctx, environmentSelect+` WHERE project_id = ? ORDER BY requested_at DESC, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project environment requests: %w", err)
	}
	defer rows.Close()
	requests := make([]projectenvironment.Request, 0)
	for rows.Next() {
		request, err := scanEnvironmentRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (store *ProjectEnvironmentStore) Transition(ctx context.Context, id string, from, to projectenvironment.Status, resolved map[string]string, detail string, occurredAt time.Time) (projectenvironment.Request, bool, error) {
	current, err := store.Get(ctx, id)
	if err != nil {
		return projectenvironment.Request{}, false, err
	}
	if current.Status == to {
		return current, false, nil
	}
	if current.Status != from {
		return projectenvironment.Request{}, false, projectenvironment.ErrConflict
	}
	resolvedJSON, _ := json.Marshal(resolved)
	var completed any
	if to == projectenvironment.StatusReady || to == projectenvironment.StatusRejected {
		completed = formatExecutionTime(occurredAt)
	}
	result, err := store.db.ExecContext(ctx, `UPDATE project_environment_requests
		SET status = ?, resolved_packages_json = ?, error = ?, updated_at = ?, completed_at = ?
		WHERE id = ? AND status = ?`, to, string(resolvedJSON), detail,
		formatExecutionTime(occurredAt), completed, id, from)
	if err != nil {
		return projectenvironment.Request{}, false, fmt.Errorf("transition project environment request: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return projectenvironment.Request{}, false, projectenvironment.ErrConflict
	}
	updated, err := store.Get(ctx, id)
	return updated, true, err
}

func (store *ProjectEnvironmentStore) ListApprovedPackages(ctx context.Context) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT packages_json FROM project_environment_requests
		WHERE status IN ('approved', 'provisioning', 'ready', 'failed')`)
	if err != nil {
		return nil, fmt.Errorf("list approved environment packages: %w", err)
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var packages []string
		if err := json.Unmarshal([]byte(raw), &packages); err != nil {
			return nil, fmt.Errorf("decode approved environment packages: %w", err)
		}
		for _, name := range packages {
			seen[name] = struct{}{}
		}
	}
	packages := make([]string, 0, len(seen))
	for name := range seen {
		packages = append(packages, name)
	}
	return packages, rows.Err()
}

const environmentSelect = `SELECT id, project_id, feature_id, run_id, session_id, attempt_id,
	packages_json, reason, status, resolved_packages_json, error,
	requested_at, updated_at, completed_at FROM project_environment_requests`

type environmentScanner interface{ Scan(...any) error }

func scanEnvironmentRequest(scanner environmentScanner) (projectenvironment.Request, error) {
	var request projectenvironment.Request
	var packagesJSON, resolvedJSON, requestedAt, updatedAt string
	var completedAt sql.NullString
	if err := scanner.Scan(&request.ID, &request.ProjectID, &request.FeatureID, &request.RunID,
		&request.SessionID, &request.AttemptID, &packagesJSON, &request.Reason, &request.Status,
		&resolvedJSON, &request.Error, &requestedAt, &updatedAt, &completedAt); err != nil {
		return projectenvironment.Request{}, err
	}
	if err := json.Unmarshal([]byte(packagesJSON), &request.SystemPackages); err != nil {
		return projectenvironment.Request{}, err
	}
	if err := json.Unmarshal([]byte(resolvedJSON), &request.ResolvedPackages); err != nil {
		return projectenvironment.Request{}, err
	}
	var err error
	request.RequestedAt, err = parseExecutionTime(requestedAt)
	if err != nil {
		return projectenvironment.Request{}, err
	}
	request.UpdatedAt, err = parseExecutionTime(updatedAt)
	if err != nil {
		return projectenvironment.Request{}, err
	}
	if completedAt.Valid {
		value, err := parseExecutionTime(completedAt.String)
		if err != nil {
			return projectenvironment.Request{}, err
		}
		request.CompletedAt = &value
	}
	if err := request.Validate(); err != nil {
		return projectenvironment.Request{}, err
	}
	return request, nil
}
