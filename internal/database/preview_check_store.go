package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
)

type PreviewCheckStore struct{ db *sql.DB }

var _ previewcheck.Store = (*PreviewCheckStore)(nil)

func NewPreviewCheckStore(db *sql.DB) *PreviewCheckStore { return &PreviewCheckStore{db: db} }

func (store *PreviewCheckStore) Create(ctx context.Context, check previewcheck.Check) (previewcheck.Check, bool, error) {
	result, err := store.db.ExecContext(ctx, `INSERT INTO preview_checks (
		id, project_id, feature_id, run_id, commit_id, attempt, status, evidence_json,
		created_at, updated_at, claimed_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, NULL, NULL) ON CONFLICT(id) DO NOTHING`,
		check.ID, check.ProjectID, check.FeatureID, check.RunID, check.CommitID, check.Attempt, check.Status,
		formatExecutionTime(check.CreatedAt), formatExecutionTime(check.UpdatedAt))
	if err != nil {
		return previewcheck.Check{}, false, fmt.Errorf("create preview check: %w", err)
	}
	count, _ := result.RowsAffected()
	stored, err := store.Get(ctx, check.ID)
	if err != nil {
		return previewcheck.Check{}, false, err
	}
	if count == 0 && (stored.RunID != check.RunID || stored.CommitID != check.CommitID) {
		return previewcheck.Check{}, false, previewcheck.ErrConflict
	}
	return stored, count == 1, nil
}

func (store *PreviewCheckStore) Get(ctx context.Context, id string) (previewcheck.Check, error) {
	check, err := scanPreviewCheck(store.db.QueryRowContext(ctx, previewCheckSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return previewcheck.Check{}, previewcheck.ErrNotFound
	}
	return check, err
}

func (store *PreviewCheckStore) ListByRun(ctx context.Context, runID string) ([]previewcheck.Check, error) {
	return store.list(ctx, previewCheckSelect+` WHERE run_id = ? ORDER BY created_at, id`, runID)
}

// ListUnfinished lists the pending and running checks, oldest first.
func (store *PreviewCheckStore) ListUnfinished(ctx context.Context) ([]previewcheck.Check, error) {
	return store.list(ctx, previewCheckSelect+` WHERE status IN (?, ?) ORDER BY created_at, id`,
		previewcheck.StatusPending, previewcheck.StatusRunning)
}

func (store *PreviewCheckStore) list(ctx context.Context, query string, args ...any) ([]previewcheck.Check, error) {
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list preview checks: %w", err)
	}
	defer rows.Close()
	checks := make([]previewcheck.Check, 0)
	for rows.Next() {
		check, err := scanPreviewCheck(rows)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

func (store *PreviewCheckStore) Transition(
	ctx context.Context,
	id string,
	from, to previewcheck.Status,
	evidence *previewcheck.Evidence,
	occurredAt time.Time,
) (previewcheck.Check, bool, error) {
	current, err := store.Get(ctx, id)
	if err != nil {
		return previewcheck.Check{}, false, err
	}
	if current.Status == to {
		return current, false, nil
	}
	if current.Status != from {
		return previewcheck.Check{}, false, previewcheck.ErrConflict
	}
	evidenceJSON := ""
	if evidence != nil {
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return previewcheck.Check{}, false, fmt.Errorf("encode preview check evidence: %w", err)
		}
		evidenceJSON = string(encoded)
	}
	at := formatExecutionTime(occurredAt)
	var claimed, completed any
	if current.ClaimedAt != nil {
		claimed = formatExecutionTime(*current.ClaimedAt)
	}
	switch to {
	case previewcheck.StatusRunning:
		claimed = at
	case previewcheck.StatusPassed, previewcheck.StatusFailed, previewcheck.StatusExpired:
		completed = at
	}
	result, err := store.db.ExecContext(ctx, `UPDATE preview_checks SET status = ?, evidence_json = ?,
		updated_at = ?, claimed_at = ?, completed_at = ? WHERE id = ? AND status = ?`,
		to, evidenceJSON, at, claimed, completed, id, from)
	if err != nil {
		return previewcheck.Check{}, false, fmt.Errorf("update preview check: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return previewcheck.Check{}, false, previewcheck.ErrConflict
	}
	updated, err := store.Get(ctx, id)
	return updated, true, err
}

const previewCheckSelect = `SELECT id, project_id, feature_id, run_id, commit_id, attempt, status,
	evidence_json, created_at, updated_at, claimed_at, completed_at FROM preview_checks`

func scanPreviewCheck(scanner validationScanner) (previewcheck.Check, error) {
	var check previewcheck.Check
	var evidenceJSON, createdAt, updatedAt string
	var claimedAt, completedAt sql.NullString
	if err := scanner.Scan(&check.ID, &check.ProjectID, &check.FeatureID, &check.RunID, &check.CommitID,
		&check.Attempt, &check.Status, &evidenceJSON, &createdAt, &updatedAt, &claimedAt, &completedAt); err != nil {
		return previewcheck.Check{}, err
	}
	if evidenceJSON != "" {
		var evidence previewcheck.Evidence
		if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
			return previewcheck.Check{}, fmt.Errorf("decode preview check evidence: %w", err)
		}
		if evidence.Services == nil {
			evidence.Services = []previewcheck.ServiceEvidence{}
		}
		check.Evidence = &evidence
	}
	var err error
	if check.CreatedAt, err = parseExecutionTime(createdAt); err != nil {
		return previewcheck.Check{}, err
	}
	if check.UpdatedAt, err = parseExecutionTime(updatedAt); err != nil {
		return previewcheck.Check{}, err
	}
	for _, field := range []struct {
		value  sql.NullString
		target **time.Time
	}{{claimedAt, &check.ClaimedAt}, {completedAt, &check.CompletedAt}} {
		if !field.value.Valid {
			continue
		}
		parsed, err := parseExecutionTime(field.value.String)
		if err != nil {
			return previewcheck.Check{}, err
		}
		*field.target = &parsed
	}
	if err := check.Validate(); err != nil {
		return previewcheck.Check{}, err
	}
	return check, nil
}
